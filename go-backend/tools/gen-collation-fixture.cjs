#!/usr/bin/env node
'use strict'

// Generates go-backend/internal/textsort/testdata/collation.json from Node's
// own Intl, the behavior oracle for internal/textsort. For every ordered pair
// of sample strings it records the sign of
//   a.localeCompare(b)                                   (Playlist.js name sort)
//   Intl.Collator(undefined, {numeric, base}).compare    (libraryHelpers naturalSort)
// Usage: node tools/gen-collation-fixture.cjs [out.json]

const fs = require('node:fs')
const path = require('node:path')

const words = [
  'apple', 'Apple', 'APPLE', 'banana', 'Banana', 'cherry', 'Éclair', 'eclair', 'Eclair', 'zebra', 'Zebra',
  'ärger', 'arger', 'Arger', 'ångström', 'angstrom', 'Zoë', 'zoe', 'Zoe',
  'a', 'A', 'b', 'B', '1', '2', '10', '11', '100', '1.5', '1.25', '01', '001', '2.5',
  'Book 2', 'Book 10', 'book 1', 'Book 1', 'Book 1.5', 'Book 02',
  'The Beginning', 'the beginning', 'Beginning, The', '', ' ', '-', '_', '!', '#', '(x)', '[x]',
  'x y', 'x-y', 'xy', 'x_y', 'x.y', 'x,y', "o'neil", 'oneil', 'O’Neil',
  '1984', '19th Century', '2 Fast', '20,000 Leagues', 'Ünder', 'under', 'Zürich', 'zurich',
  'straße', 'strasse', 'ß', 'ss', 'Œuvre', 'oeuvre', 'Æon', 'aeon', 'ñandú', 'nandu', 'Ñ', 'n',
  'o', 'ö', 'Ö', 'ø', 'Ø', 'å', 'z', 'Commute', 'commute', 'Road Trip', 'road trip', 'Évening',
  'Zzz', 'Favourites', 'favorites', '$5', '€5', '5', 'Привет', 'привет', 'Ελλάδα',
  'null', 'undefined', '3', '3.0', '3.10', '3.9', '12 Angry Men', '2 Broke Girls', 'a1', 'a01', 'a10', 'a2'
]
const natural = new Intl.Collator(undefined, { numeric: true, sensitivity: 'base' }).compare
const pairs = []
for (const a of words) {
  for (const b of words) pairs.push([a, b, Math.sign(a.localeCompare(b)), Math.sign(natural(a, b))])
}
const out = process.argv[2] || path.join(__dirname, '..', 'internal', 'textsort', 'testdata', 'collation.json')
fs.writeFileSync(out, JSON.stringify({ node: process.version, icu: process.versions.icu, pairs }))
console.log(`wrote ${pairs.length} pairs to ${out} (node ${process.version}, ICU ${process.versions.icu})`)
