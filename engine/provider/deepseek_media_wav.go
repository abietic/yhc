package provider

import (
	"encoding/binary"
	"fmt"
)

const wavStreamingSize = uint32(0xffffffff)

type deepSeekWAV struct {
	sampleRate    uint64
	channels      uint64
	frameSize     uint64
	frames        uint64
	dataSizeField int
	dataSize      uint32
	streaming     bool
}

// readDeepSeekWAV reads a single PCM data chunk. ASR requires a fully sized
// container, while TTS may return a streaming WAV with both size sentinels.
func readDeepSeekWAV(wav []byte, allowStreaming bool) (deepSeekWAV, error) {
	var info deepSeekWAV
	invalid := fmt.Errorf("deepseek media: incomplete or unsupported PCM WAV")
	if len(wav) < 12 || string(wav[:4]) != "RIFF" || string(wav[8:12]) != "WAVE" {
		return info, invalid
	}
	riffSize := binary.LittleEndian.Uint32(wav[4:8])
	info.streaming = riffSize == wavStreamingSize
	if info.streaming {
		if !allowStreaming {
			return info, invalid
		}
	} else if uint64(riffSize)+8 != uint64(len(wav)) {
		return info, invalid
	}

	for offset := 12; offset < len(wav); {
		if len(wav)-offset < 8 {
			return info, invalid
		}
		kind := string(wav[offset : offset+4])
		size := uint64(binary.LittleEndian.Uint32(wav[offset+4 : offset+8]))
		start := offset + 8
		if size == uint64(wavStreamingSize) && kind == "data" && info.streaming {
			size = uint64(len(wav) - start)
		} else if kind == "data" && info.streaming {
			return info, invalid
		}
		padded := size + size%2
		if padded > uint64(len(wav)-start) {
			return info, invalid
		}
		switch kind {
		case "fmt ":
			if info.sampleRate != 0 || size < 16 {
				return info, invalid
			}
			b := wav[start : start+16]
			info.channels = uint64(binary.LittleEndian.Uint16(b[2:4]))
			info.sampleRate = uint64(binary.LittleEndian.Uint32(b[4:8]))
			info.frameSize = uint64(binary.LittleEndian.Uint16(b[12:14]))
			bytesPerSecond := uint64(binary.LittleEndian.Uint32(b[8:12]))
			if binary.LittleEndian.Uint16(b[:2]) != 1 || binary.LittleEndian.Uint16(b[14:16]) != 16 ||
				info.channels < 1 || info.channels > 2 || info.sampleRate == 0 ||
				info.frameSize != info.channels*2 || bytesPerSecond != info.sampleRate*info.frameSize {
				return info, invalid
			}
		case "data":
			if info.frameSize == 0 || info.frames != 0 || size == 0 || size%info.frameSize != 0 {
				return info, invalid
			}
			info.frames = size / info.frameSize
			info.dataSizeField = offset + 4
			info.dataSize = uint32(size)
		}
		offset = start + int(padded)
	}
	if info.frames == 0 {
		return info, invalid
	}
	return info, nil
}

func finalizeDeepSeekWAV(wav []byte, framed bool) error {
	info, err := readDeepSeekWAV(wav, framed)
	if err != nil {
		return err
	}
	if info.channels != 1 || info.sampleRate != 24000 {
		return fmt.Errorf("deepseek TTS: unexpected audio format")
	}
	// A successful framed HTTP response proves the body ended without a
	// connection truncation. Fill in the sizes used by regular WAV players.
	if info.streaming {
		binary.LittleEndian.PutUint32(wav[4:8], uint32(len(wav)-8))
		binary.LittleEndian.PutUint32(wav[info.dataSizeField:info.dataSizeField+4], info.dataSize)
	}
	return nil
}
