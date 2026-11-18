'use strict'

// Media generation and tree helpers for scan-trial.cjs (new content and library
// maintenance). Copied in style from replacement-trial.cjs; kept separate so the
// two trials can evolve independently.

const fs = require('node:fs')
const path = require('node:path')
const { spawnSync } = require('node:child_process')

// Every generated file and directory gets one of these mtimes so that item and
// library-file mtimeMs values are identical in the upstream and Go runs
// instead of depending on when ffmpeg happened to run.
const FILE_MTIME_S = 1700000000
const DIR_MTIME_S = 1700000100

function sh(cmd, args, opts = {}) {
  const r = spawnSync(cmd, args, { encoding: 'utf8', ...opts })
  if (r.status !== 0) throw new Error(`${cmd} ${args.join(' ')} failed: ${r.stderr || r.stdout}`)
  return r.stdout
}

function ffmpeg(args) { sh('ffmpeg', ['-hide_banner', '-loglevel', 'error', '-y', ...args]) }

function mp3(file, { freq, seconds, title, artist, album, track, tracks, extra = [] }) {
  ffmpeg(['-f', 'lavfi', '-i', `sine=frequency=${freq}:duration=${seconds}`, '-ac', '1', '-ar', '22050', '-b:a', '32k',
    '-metadata', `title=${title}`, '-metadata', `artist=${artist}`, '-metadata', `album=${album}`,
    '-metadata', `track=${track}/${tracks}`, ...extra, '-id3v2_version', '3', file])
}

function jpg(file, color, size = '600x600') {
  ffmpeg(['-f', 'lavfi', '-i', `color=c=${color}:s=${size}`, '-frames:v', '1', file])
}

// The seeded library: three books.
function generateSeedMedia(mediaRoot) {
  const books = path.join(mediaRoot, 'audiobooks')
  const scratch = path.join(mediaRoot, '..', 'gen')
  fs.mkdirSync(scratch, { recursive: true })

  const saga1 = path.join(books, 'Terry Fixture', 'Long Saga', 'Book 1 - The Beginning')
  fs.mkdirSync(saga1, { recursive: true })
  for (let i = 1; i <= 3; i++) {
    mp3(path.join(saga1, `0${i} - Part ${i}.mp3`), { freq: 300 + i * 100, seconds: 20, title: `Part ${i}`, artist: 'Terry Fixture', album: 'The Beginning', track: i, tracks: 3 })
  }
  jpg(path.join(saga1, 'cover.jpg'), 'navy')

  const saga2 = path.join(books, 'Terry Fixture', 'Long Saga', 'Book 2 - The Middle')
  fs.mkdirSync(saga2, { recursive: true })
  ffmpeg(['-f', 'lavfi', '-i', 'sine=frequency=550:duration=30', '-ac', '1', '-ar', '22050',
    '-metadata', 'title=The Middle', '-metadata', 'artist=Terry Fixture', path.join(saga2, 'The Middle.flac')])
  fs.writeFileSync(path.join(saga2, 'desc.txt'), 'The second book of the saga, narrated slowly.')
  fs.writeFileSync(path.join(saga2, 'reader.txt'), 'Nora Narrator')

  const novel = path.join(books, 'Ada Example', 'Single File Novel')
  fs.mkdirSync(novel, { recursive: true })
  const chapters = path.join(scratch, 'chapters.txt')
  fs.writeFileSync(chapters, [';FFMETADATA1', 'title=Single File Novel', 'artist=Ada Example', 'album=Single File Novel',
    'genre=Fiction', 'date=2021',
    '[CHAPTER]', 'TIMEBASE=1/1000', 'START=0', 'END=15000', 'title=Opening',
    '[CHAPTER]', 'TIMEBASE=1/1000', 'START=15000', 'END=30000', 'title=Middle',
    '[CHAPTER]', 'TIMEBASE=1/1000', 'START=30000', 'END=45000', 'title=Ending', ''].join('\n'))
  const embedCover = path.join(scratch, 'embed.jpg')
  jpg(embedCover, 'maroon', '400x400')
  ffmpeg(['-f', 'lavfi', '-i', 'sine=frequency=440:duration=45', '-i', chapters, '-i', embedCover,
    '-map', '0:a', '-map', '2:v', '-map_metadata', '1', '-map_chapters', '1',
    '-c:a', 'aac', '-b:a', '32k', '-ac', '1', '-c:v', 'mjpeg', '-disposition:v', 'attached_pic',
    '-f', 'mp4', path.join(novel, 'Single File Novel.m4b')])
  return books
}

// Content added during the scenario, generated once and copied (with mtimes)
// into the library by both runs so the files are byte-identical.
function generateStage(stageRoot) {
  fs.mkdirSync(stageRoot, { recursive: true })

  // Step a: a new multi-file mp3 book with ID3 tags and a cover, in a new
  // Author/Series/Title layout.
  const dawn = path.join(stageRoot, 'newbook', 'Nora Writer', 'Fresh Arrivals', 'Vol 1 - Dawn Chorus')
  fs.mkdirSync(dawn, { recursive: true })
  for (let i = 1; i <= 3; i++) {
    mp3(path.join(dawn, `0${i} - Chorus ${i}.mp3`), {
      freq: 250 + i * 90, seconds: 12 + i, title: `Dawn Chorus Part ${i}`, artist: 'Nora Writer', album: 'Dawn Chorus', track: i, tracks: 3,
      extra: ['-metadata', 'genre=Fantasy', '-metadata', 'date=2022', '-metadata', 'composer=Reader One', '-metadata', 'comment=A fresh arrival']
    })
  }
  jpg(path.join(dawn, 'cover.jpg'), 'teal')

  // Step b: extra track and desc.txt for an existing book.
  const extra = path.join(stageRoot, 'extra')
  fs.mkdirSync(extra, { recursive: true })
  mp3(path.join(extra, '04 - Part 4.mp3'), { freq: 700, seconds: 20, title: 'Part 4', artist: 'Terry Fixture', album: 'The Beginning', track: 4, tracks: 4 })
  fs.writeFileSync(path.join(extra, 'desc.txt'), 'The first book of the saga, now with a fourth part.')

  // Step e: replacement cover file for the new book; upload fixture for step f.
  jpg(path.join(stageRoot, 'replacement-cover.jpg'), 'orange', '500x500')
  jpg(path.join(stageRoot, 'upload-cover.jpg'), 'purple', '700x700')

  // Watcher step: a book that appears with no scan request.
  const watched = path.join(stageRoot, 'watched', 'Wanda Watcher', 'Watched Folder')
  fs.mkdirSync(watched, { recursive: true })
  for (let i = 1; i <= 2; i++) {
    mp3(path.join(watched, `0${i} - Watch ${i}.mp3`), { freq: 200 + i * 110, seconds: 10, title: `Watch ${i}`, artist: 'Wanda Watcher', album: 'Watched Folder', track: i, tracks: 2 })
  }
  jpg(path.join(watched, 'cover.jpg'), 'olive', '300x300')
}

function walk(dir, visit) {
  for (const entry of fs.readdirSync(dir, { withFileTypes: true })) {
    const full = path.join(dir, entry.name)
    if (entry.isDirectory()) walk(full, visit)
    visit(full, entry.isDirectory())
  }
}

// Pin mtimes so identical content has identical stat data in both runs.
function pinMtimes(root) {
  walk(root, (p, isDir) => {
    const t = isDir ? DIR_MTIME_S : FILE_MTIME_S
    fs.utimesSync(p, t, t)
  })
  fs.utimesSync(root, DIR_MTIME_S, DIR_MTIME_S)
}

// cp -a keeps file and directory mtimes; inodes are new (documented: ino fields
// are normalized, and the scenario starts with a baseline rescan so stored
// inodes match the restored tree before any step relies on them).
function copyTree(from, to) {
  fs.rmSync(to, { recursive: true, force: true })
  fs.mkdirSync(path.dirname(to), { recursive: true })
  sh('cp', ['-a', from, to])
}

function copyInto(from, toDir) {
  fs.mkdirSync(toDir, { recursive: true })
  sh('cp', ['-a', from, toDir])
}

function setMtime(p, seconds = DIR_MTIME_S) { fs.utimesSync(p, seconds, seconds) }

module.exports = { sh, ffmpeg, generateSeedMedia, generateStage, pinMtimes, copyTree, copyInto, setMtime, FILE_MTIME_S, DIR_MTIME_S }
