package podcast

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"math"
	"strings"
)

// testToneWAV is a mono 8 kHz sine wave of the given length in seconds.
func testToneWAV(duration float64) []byte {
	const rate = 8000
	frames := int(duration * rate)
	buf := new(bytes.Buffer)
	buf.WriteString("RIFF")
	_ = binary.Write(buf, binary.LittleEndian, uint32(36+frames*2))
	buf.WriteString("WAVEfmt ")
	for _, v := range []any{uint32(16), uint16(1), uint16(1), uint32(rate), uint32(rate * 2), uint16(2), uint16(16)} {
		_ = binary.Write(buf, binary.LittleEndian, v)
	}
	buf.WriteString("data")
	_ = binary.Write(buf, binary.LittleEndian, uint32(frames*2))
	for i := range frames {
		_ = binary.Write(buf, binary.LittleEndian, int16(math.Sin(2*math.Pi*440*float64(i)/rate)*6000))
	}
	return buf.Bytes()
}

// feedXML is an RSS feed holding the items.
func feedXML(items ...string) string {
	return `<?xml version="1.0"?><rss version="2.0" xmlns:itunes="http://www.itunes.com/dtds/podcast-1.0.dtd"><channel><title>Fixture Cast</title><description>About</description><itunes:author>Ann</itunes:author>` + strings.Join(items, "") + `</channel></rss>`
}

// feedItem is an RSS item whose enclosure points at path on the server that
// replaces the EPIS placeholder.
func feedItem(title, guid, date, path string) string {
	return fmt.Sprintf(`<item><title>%s</title><guid>%s</guid><pubDate>%s</pubDate><itunes:season>1</itunes:season><itunes:subtitle>sub %s</itunes:subtitle><description>&lt;p&gt;d %s&lt;/p&gt;</description><enclosure url="EPIS%s" type="audio/wav" length="2000"/></item>`, title, guid, date, title, title, path)
}
