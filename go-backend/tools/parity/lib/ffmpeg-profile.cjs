'use strict'

const { execFileSync } = require('node:child_process')

function advertisedProfile(output) {
  return { libwebpAdvertised: /\blibwebp\b/.test(output) }
}

function withEncodeProbe(profile, succeeded) {
  if (profile.libwebpAdvertised && !succeeded) throw new Error('FFmpeg capability prerequisite failed: libwebp is advertised but the synthetic encode probe failed')
  return { ...profile, webpEncodeProbe: profile.libwebpAdvertised && succeeded, webp: profile.libwebpAdvertised && succeeded }
}

function webpExpectedStatus(profile) {
  return profile.webp ? 200 : 500
}

function detectFFmpegProfile() {
  const ffmpeg = process.env.FFMPEG || 'ffmpeg'
  let output
  try {
    output = execFileSync(ffmpeg, ['-hide_banner', '-encoders'], { encoding: 'utf8', stdio: ['ignore', 'pipe', 'pipe'], timeout: 10000 })
  } catch (error) {
    throw new Error(`FFmpeg capability prerequisite failed: could not inspect encoders (${error.message})`)
  }
  const profile = advertisedProfile(output)
  if (!profile.libwebpAdvertised) return withEncodeProbe(profile, false)
  try {
    execFileSync(ffmpeg, ['-v', 'error', '-f', 'lavfi', '-i', 'color=c=red:s=8x8', '-frames:v', '1', '-c:v', 'libwebp', '-f', 'webp', 'pipe:1'], { stdio: ['ignore', 'ignore', 'pipe'], timeout: 10000, maxBuffer: 1024 * 1024 })
  } catch (error) {
    throw new Error(`FFmpeg capability prerequisite failed: libwebp is advertised but the synthetic encode probe failed (${error.message})`)
  }
  return withEncodeProbe(profile, true)
}

module.exports = { advertisedProfile, withEncodeProbe, webpExpectedStatus, detectFFmpegProfile }
