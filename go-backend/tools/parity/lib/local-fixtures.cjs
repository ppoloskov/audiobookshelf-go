'use strict'

const fs = require('node:fs')
const path = require('node:path')

async function startCoverSource(file) {
  const server = require('node:http').createServer((req, res) => {
    if (req.url === '/redirect-307.png') {
      res.writeHead(307, { location: '/cover.png' })
      return res.end()
    }
    if (req.url === '/unsupported-format') {
      const unsupported = Buffer.from('FLIF')
      res.writeHead(200, { 'content-type': 'image/flif', 'content-length': String(unsupported.length) })
      return res.end(unsupported)
    }
    if (req.url !== '/cover.png') { res.writeHead(404); return res.end() }
    res.writeHead(200, { 'content-type': 'image/png', 'content-length': String(fs.statSync(file).size) })
    fs.createReadStream(file).pipe(res)
  })
  const port = await new Promise((resolve, reject) => server.listen(0, '127.0.0.1', (err) => err ? reject(err) : resolve(server.address().port)))
  server.unref()
  return { server, url: `http://127.0.0.1:${port}/cover.png` }
}

async function startSMTPFixture({ rejectConnection = false } = {}) {
  const net = require('node:net')
  const sessions = []
  const server = net.createServer((socket) => {
    const session = { mailFrom: null, recipients: [], message: '' }
    sessions.push(session)
    let input = ''
    let inData = false
    if (rejectConnection) {
      socket.end('421 4.3.0 service unavailable\r\n')
      return
    }
    socket.write('220 parity-smtp.local ESMTP\r\n')
    socket.on('data', chunk => {
      input += chunk.toString('utf8')
      while (true) {
        if (inData) {
          const end = input.indexOf('\r\n.\r\n')
          if (end < 0) break
          session.message += input.slice(0, end + 2)
          input = input.slice(end + 5)
          inData = false
          socket.write('250 2.0.0 queued\r\n')
          continue
        }
        const end = input.indexOf('\r\n')
        if (end < 0) break
        const line = input.slice(0, end)
        input = input.slice(end + 2)
        const command = line.split(' ', 1)[0].toUpperCase()
        if (command === 'EHLO' || command === 'HELO') socket.write('250-parity-smtp.local\r\n250 PIPELINING\r\n')
        else if (command === 'MAIL') {
          session.mailFrom = line.slice(line.indexOf(':') + 1).replace(/[<>]/g, '').trim()
          socket.write('250 2.1.0 sender ok\r\n')
        } else if (command === 'RCPT') {
          session.recipients.push(line.slice(line.indexOf(':') + 1).replace(/[<>]/g, '').trim())
          socket.write('250 2.1.5 recipient ok\r\n')
        } else if (command === 'DATA') {
          inData = true
          socket.write('354 end with <CRLF>.<CRLF>\r\n')
        } else if (command === 'QUIT') {
          socket.end('221 2.0.0 bye\r\n')
        } else socket.write('250 2.0.0 ok\r\n')
      }
    })
  })
  const port = await new Promise((resolve, reject) => server.listen(0, '127.0.0.1', error => error ? reject(error) : resolve(server.address().port)))
  server.unref()
  return { server, port, sessions }
}

async function startNotificationAppriseFixture() {
  const requests = { node: [], go: [] }
  const server = require('node:http').createServer((req, res) => {
    const side = req.url.split('/')[1]
    const chunks = []
    req.on('data', chunk => chunks.push(chunk))
    req.on('end', () => {
      if (!Object.hasOwn(requests, side)) {
        res.writeHead(404)
        return res.end()
      }
      let payload = null
      try { payload = JSON.parse(Buffer.concat(chunks).toString('utf8')) } catch {}
      requests[side].push({ path: req.url, payload })
      res.writeHead(req.url.endsWith('/failure') ? 503 : 200, { 'content-type': 'text/plain' })
      res.end(req.url.endsWith('/failure') ? 'fixture failure' : 'sent')
    })
  })
  const port = await new Promise((resolve, reject) => server.listen(0, '127.0.0.1', error => error ? reject(error) : resolve(server.address().port)))
  server.unref()
  return { server, requests, url: `http://127.0.0.1:${port}` }
}

function smtpMessageShape(session) {
  const normalized = session.message.replace(/\r\n/g, '\n')
  const split = normalized.indexOf('\n\n')
  const headers = Object.fromEntries(normalized.slice(0, split).split('\n').map(line => {
    const colon = line.indexOf(':')
    return [line.slice(0, colon).toLowerCase(), line.slice(colon + 1).trim()]
  }))
  return {
    mailFrom: session.mailFrom,
    recipients: session.recipients,
    from: headers.from,
    to: headers.to,
    subject: headers.subject,
    contentType: headers['content-type'],
    body: normalized.slice(split + 2).replace(/\n$/, '')
  }
}

async function readEmailDeviceState(directory) {
  const sqlite = require('sqlite3').verbose()
  const db = new sqlite.Database(path.join(directory, 'config', 'absdatabase.sqlite'))
  try {
    const row = await new Promise((resolve, reject) => db.get(
      "SELECT value,updatedAt FROM settings WHERE key='email-settings'",
      (err, value) => err ? reject(err) : resolve(value)
    ))
    if (!row) return { updatedAt: null, devices: [] }
    const settings = JSON.parse(row.value)
    return { updatedAt: row.updatedAt, devices: settings.ereaderDevices ?? [] }
  } finally {
    await new Promise((resolve) => db.close(resolve))
  }
}

async function readCoverDeleteState(directory, itemID) {
  const sqlite = require('sqlite3').verbose()
  const db = new sqlite.Database(path.join(directory, 'config', 'absdatabase.sqlite'))
  let coverPath
  try {
    coverPath = await new Promise((resolve, reject) => db.get(
      'SELECT b.coverPath FROM libraryItems li JOIN books b ON b.id=li.mediaId WHERE li.id=?',
      [itemID],
      (err, row) => err ? reject(err) : resolve(row?.coverPath)
    ))
  } finally {
    await new Promise((resolve) => db.close(resolve))
  }
  const cacheDir = path.join(directory, 'metadata', 'cache', 'covers')
  const cacheFiles = fs.existsSync(cacheDir)
    ? fs.readdirSync(cacheDir).filter((name) => name.startsWith(`${itemID}_`)).sort()
    : []
  const coverDir = path.join(directory, 'metadata', 'items', itemID)
  const coverFiles = fs.existsSync(coverDir)
    ? fs.readdirSync(coverDir).filter((name) => /^cover\./i.test(name)).sort()
    : []
  return { coverPath: coverPath ?? null, cacheFiles, coverFiles }
}

module.exports = { startCoverSource, startSMTPFixture, startNotificationAppriseFixture, smtpMessageShape, readEmailDeviceState, readCoverDeleteState }
