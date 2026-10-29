package service

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"net/http"
	"strings"
)

var errAssetMediaType = errors.New("unsupported or indeterminate material format")

// Container brands describe a format, not its tracks. Both explicit and
// automatic requests inspect these containers before applying a media limit.
func sniffAssetMedia(data []byte) (mime, container string) {
	mime = http.DetectContentType(data)
	if len(data) >= 12 && string(data[4:8]) == "ftyp" {
		switch string(data[8:12]) {
		case "heic", "heix", "hevc", "hevx", "mif1", "msf1":
			return "image/heif", ""
		case "avif", "avis":
			return "image/avif", ""
		case "qt  ":
			return "", "quicktime"
		case "M4A ", "M4B ":
			return "", "mp4"
		default:
			if mime == "video/mp4" {
				return "", "mp4"
			}
			return "", ""
		}
	}
	if bytes.HasPrefix(data, []byte("\x1a\x45\xdf\xa3")) {
		return "", "webm"
	}
	if bytes.HasPrefix(data, []byte("OggS")) {
		return "", "ogg"
	}
	if len(data) >= 12 && string(data[:4]) == "RIFF" && string(data[8:12]) == "AVI " {
		return "", "avi"
	}
	// net/http recognizes only a subset of common media signatures. Keep the
	// existing frame markers; filenames and caller Content-Type are not evidence.
	if len(data) >= 7 && data[0] == 0xff {
		if data[1]&0xf6 == 0xf0 {
			mime = "audio/aac"
		} else if data[1]&0xe0 == 0xe0 && data[1]&0x06 != 0 && data[1]&0x18 != 8 {
			mime = "audio/mpeg"
		}
	}
	if bytes.HasPrefix(data, []byte("fLaC")) {
		mime = "audio/flac"
	}
	return mime, ""
}

func assetMediaType(mime string) string {
	switch {
	case strings.HasPrefix(mime, "image/"):
		return "Image"
	case strings.HasPrefix(mime, "video/"):
		return "Video"
	case strings.HasPrefix(mime, "audio/"):
		return "Audio"
	default:
		return ""
	}
}

// This is a bounded track classifier, not a decoder. It visits only the
// format's fixed metadata hierarchy, skips media payloads, and refuses missing
// or unknown tracks. Limits apply across the whole file, including chained Ogg.
type assetMediaProbe struct {
	body     io.ReadSeeker
	start    int64
	size     int64
	bytes    int
	elements int
}

func (probe *assetMediaProbe) read(offset int64, count int, end int64) ([]byte, error) {
	if offset < 0 || end > probe.size || offset > end || int64(count) > end-offset || count < 0 || count > probe.bytes {
		return nil, errAssetMediaType
	}
	probe.bytes -= count
	if _, err := probe.body.Seek(probe.start+offset, io.SeekStart); err != nil {
		return nil, err
	}
	data := make([]byte, count)
	_, err := io.ReadFull(probe.body, data)
	return data, err
}

func (probe *assetMediaProbe) element() error {
	probe.elements--
	if probe.elements < 0 {
		return errAssetMediaType
	}
	return nil
}

func detectAssetContainer(body io.ReadSeeker, start, size int64, format string) (string, error) {
	probe := assetMediaProbe{body: body, start: start, size: size, bytes: 1 << 20, elements: 4096}
	var kind string
	var err error
	switch format {
	case "mp4", "quicktime":
		kind, err = probe.mp4Tracks(0, size, 0)
	case "webm":
		kind, err = probe.webmTracks()
	case "ogg":
		kind, err = probe.oggTracks()
	case "avi":
		kind, err = probe.aviTracks(0, size, 0)
	default:
		err = errAssetMediaType
	}
	// These combinations were not accepted media formats before automatic
	// detection. Knowing their category must not silently add format support.
	if err != nil || kind == "" || (format == "ogg" && kind == "Video") || ((format == "avi" || format == "quicktime") && kind == "Audio") {
		return "", errAssetMediaType
	}
	return strings.ToLower(kind) + "/" + format, nil
}

// ISO BMFF/QuickTime handler types live only at moov/trak/mdia/hdlr. Never
// search payload bytes: mdat may contain arbitrary strings resembling tracks.
func (probe *assetMediaProbe) mp4Tracks(offset, end int64, depth int) (string, error) {
	expected := [...]string{"moov", "trak", "mdia", "hdlr"}[depth]
	kind, matches := "Audio", 0
	for offset < end {
		if err := probe.element(); err != nil {
			return "", err
		}
		header, err := probe.read(offset, 8, end)
		if err != nil {
			return "", err
		}
		length, headerSize := uint64(binary.BigEndian.Uint32(header[:4])), int64(8)
		if length == 1 {
			extended, err := probe.read(offset+8, 8, end)
			if err != nil {
				return "", err
			}
			length, headerSize = binary.BigEndian.Uint64(extended), 16
		} else if length == 0 {
			length = uint64(end - offset)
		}
		if length < uint64(headerSize) || length > uint64(end-offset) {
			return "", errAssetMediaType
		}
		next := offset + int64(length)
		if string(header[4:8]) == expected {
			matches++
			if depth != 1 && matches > 1 {
				return "", errAssetMediaType
			}
			track := ""
			if depth == 3 {
				handler, err := probe.read(offset+headerSize, 12, next)
				if err != nil {
					return "", err
				}
				switch string(handler[8:12]) {
				case "vide":
					track = "Video"
				case "soun":
					track = "Audio"
				default:
					return "", errAssetMediaType
				}
			} else {
				track, err = probe.mp4Tracks(offset+headerSize, next, depth+1)
				if err != nil {
					return "", err
				}
			}
			if track == "Video" {
				kind = track
			}
		}
		offset = next
	}
	if matches == 0 {
		return "", errAssetMediaType
	}
	return kind, nil
}

func (probe *assetMediaProbe) ebmlVINT(offset, end int64, identifier bool) (uint64, int64, bool, error) {
	first, err := probe.read(offset, 1, end)
	if err != nil || first[0] == 0 {
		return 0, 0, false, errAssetMediaType
	}
	width, marker := 1, byte(0x80)
	for first[0]&marker == 0 {
		width++
		marker >>= 1
	}
	if identifier && width > 4 {
		return 0, 0, false, errAssetMediaType
	}
	data, err := probe.read(offset, width, end)
	if err != nil {
		return 0, 0, false, err
	}
	value := uint64(data[0] & (marker - 1))
	if identifier {
		value = uint64(data[0])
	}
	for _, b := range data[1:] {
		value = value<<8 | uint64(b)
	}
	unknown := !identifier && value == (uint64(1)<<(7*width))-1
	return value, int64(width), unknown, nil
}

func (probe *assetMediaProbe) ebmlElement(offset, end int64) (id uint64, payload, next int64, err error) {
	if err = probe.element(); err != nil {
		return
	}
	id, idWidth, _, err := probe.ebmlVINT(offset, end, true)
	if err != nil {
		return
	}
	length, width, unknown, err := probe.ebmlVINT(offset+idWidth, end, false)
	if err != nil {
		return
	}
	payload = offset + idWidth + width
	// Unknown-size Segment is standard for streaming. Unknown-size Cluster
	// would require the complete EBML schema to find its end; fail closed.
	if unknown {
		if id != 0x18538067 {
			err = errAssetMediaType
			return
		}
		length = uint64(end - payload)
	}
	if length > uint64(end-payload) {
		err = errAssetMediaType
		return
	}
	next = payload + int64(length)
	return
}

func (probe *assetMediaProbe) webmTracks() (string, error) {
	id, payload, headerEnd, err := probe.ebmlElement(0, probe.size)
	if err != nil || id != 0x1a45dfa3 {
		return "", errAssetMediaType
	}
	docType := false
	for payload < headerEnd {
		id, start, next, err := probe.ebmlElement(payload, headerEnd)
		if err != nil {
			return "", err
		}
		if id == 0x4282 {
			if docType || next-start != 4 {
				return "", errAssetMediaType
			}
			value, err := probe.read(start, 4, next)
			if err != nil || string(value) != "webm" {
				return "", errAssetMediaType
			}
			docType = true
		}
		payload = next
	}
	if !docType {
		return "", errAssetMediaType
	}
	kind := ""
	for offset := headerEnd; offset < probe.size; {
		id, start, next, err := probe.ebmlElement(offset, probe.size)
		if err != nil {
			return "", err
		}
		if id == 0x18538067 {
			if kind != "" {
				return "", errAssetMediaType
			}
			kind, err = probe.webmTrackEntries(start, next, 0)
			if err != nil {
				return "", err
			}
		} else if id != 0xec && id != 0xbf {
			return "", errAssetMediaType
		}
		offset = next
	}
	return kind, nil
}

func (probe *assetMediaProbe) webmTrackEntries(offset, end int64, depth int) (string, error) {
	expected := [...]uint64{0x1654ae6b, 0xae, 0x83}[depth]
	kind, matches := "Audio", 0
	for offset < end {
		id, start, next, err := probe.ebmlElement(offset, end)
		if err != nil {
			return "", err
		}
		if id == expected {
			matches++
			if depth != 1 && matches > 1 {
				return "", errAssetMediaType
			}
			track := ""
			if depth == 2 {
				if next-start < 1 || next-start > 8 {
					return "", errAssetMediaType
				}
				value, err := probe.read(start, int(next-start), next)
				if err != nil {
					return "", err
				}
				var trackType uint64
				for _, b := range value {
					trackType = trackType<<8 | uint64(b)
				}
				switch trackType {
				case 1:
					track = "Video"
				case 2:
					track = "Audio"
				default:
					return "", errAssetMediaType
				}
			} else {
				track, err = probe.webmTrackEntries(start, next, depth+1)
				if err != nil {
					return "", err
				}
			}
			if track == "Video" {
				kind = track
			}
		}
		offset = next
	}
	if matches == 0 {
		return "", errAssetMediaType
	}
	return kind, nil
}

func (probe *assetMediaProbe) oggTracks() (string, error) {
	type streamState struct {
		sequence  uint32
		continued bool
		ended     bool
	}
	streams := map[uint32]streamState{}
	kind := "Audio"
	for offset := int64(0); offset < probe.size; {
		if err := probe.element(); err != nil {
			return "", err
		}
		header, err := probe.read(offset, 27, probe.size)
		if err != nil || string(header[:4]) != "OggS" || header[4] != 0 || header[5]&^byte(7) != 0 {
			return "", errAssetMediaType
		}
		lacing, err := probe.read(offset+27, int(header[26]), probe.size)
		if err != nil {
			return "", errAssetMediaType
		}
		length, firstPacket := int64(0), int64(-1)
		for _, segment := range lacing {
			length += int64(segment)
			if firstPacket < 0 && segment < 255 {
				firstPacket = length
			}
		}
		payload := offset + 27 + int64(len(lacing))
		if length > probe.size-payload {
			return "", errAssetMediaType
		}
		serial, sequence := binary.LittleEndian.Uint32(header[14:18]), binary.LittleEndian.Uint32(header[18:22])
		state, exists := streams[serial]
		if !exists {
			if header[5]&3 != 2 || sequence != 0 || firstPacket < 8 {
				return "", errAssetMediaType
			}
			codec, err := probe.read(payload, 8, payload+firstPacket)
			if err != nil {
				return "", err
			}
			switch {
			case bytes.HasPrefix(codec, []byte("\x01vorbis")), string(codec) == "OpusHead", bytes.HasPrefix(codec, []byte("\x7fFLAC")), string(codec) == "Speex   ":
			case bytes.HasPrefix(codec, []byte("\x80theora")):
				kind = "Video"
			default:
				return "", errAssetMediaType
			}
		} else if state.ended || header[5]&2 != 0 || sequence != state.sequence+1 || (header[5]&1 != 0) != state.continued {
			return "", errAssetMediaType
		}
		continued := state.continued
		if len(lacing) > 0 {
			continued = lacing[len(lacing)-1] == 255
		}
		state = streamState{sequence: sequence, continued: continued, ended: header[5]&4 != 0}
		if state.ended && state.continued {
			return "", errAssetMediaType
		}
		streams[serial] = state
		offset = payload + length
	}
	for _, state := range streams {
		if !state.ended {
			return "", errAssetMediaType
		}
	}
	return kind, nil
}

// AVI's strh stream declarations are nested under RIFF AVI / LIST hdrl /
// LIST strl. RIFF chunks are padded to even lengths; movie payloads are skipped.
func (probe *assetMediaProbe) aviTracks(offset, end int64, depth int) (string, error) {
	expected := [...]string{"AVI ", "hdrl", "strl", "strh"}[depth]
	kind, matches := "Audio", 0
	for offset < end {
		if err := probe.element(); err != nil {
			return "", err
		}
		header, err := probe.read(offset, 8, end)
		if err != nil {
			return "", err
		}
		length := int64(binary.LittleEndian.Uint32(header[4:8]))
		start := offset + 8
		if length > end-start || length+length%2 > end-start {
			return "", errAssetMediaType
		}
		next, tag := start+length, string(header[:4])
		list := tag == "RIFF" || tag == "LIST"
		if list {
			if depth == 0 && tag != "RIFF" || depth > 0 && tag != "LIST" {
				return "", errAssetMediaType
			}
			listType, err := probe.read(start, 4, next)
			if err != nil {
				return "", err
			}
			start += 4
			tag = string(listType)
		}
		if tag == expected {
			if list != (depth < 3) {
				return "", errAssetMediaType
			}
			matches++
			if depth != 2 && matches > 1 {
				return "", errAssetMediaType
			}
			track := ""
			if depth == 3 {
				streamType, err := probe.read(start, 4, next)
				if err != nil {
					return "", err
				}
				switch string(streamType) {
				case "vids", "iavs":
					track = "Video"
				case "auds":
					track = "Audio"
				default:
					return "", errAssetMediaType
				}
			} else {
				track, err = probe.aviTracks(start, next, depth+1)
				if err != nil {
					return "", err
				}
			}
			if track == "Video" {
				kind = track
			}
		}
		offset = next + length%2
	}
	if matches == 0 {
		return "", errAssetMediaType
	}
	return kind, nil
}
