package tts

import (
	"encoding/binary"
	"fmt"
	"os"
)

type wavStats struct {
	DurationMS int
	RMS        int
	Peak       int
	NearSilent bool
}

func inspectPCM16WAV(path string) (wavStats, bool, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return wavStats{}, false, fmt.Errorf("read wav response: %w", err)
	}
	info, ok := parsePCM16WAV(data)
	if !ok {
		return wavStats{}, false, nil
	}
	stats := wavStats{DurationMS: info.durationMS}
	var sumSquares uint64
	var samples int
	for i := 0; i+1 < len(info.pcm); i += 2 {
		v := int16(binary.LittleEndian.Uint16(info.pcm[i : i+2]))
		abs := int(v)
		if abs < 0 {
			abs = -abs
		}
		if abs > stats.Peak {
			stats.Peak = abs
		}
		sumSquares += uint64(int64(v) * int64(v))
		samples++
	}
	if samples > 0 {
		stats.RMS = intSqrt(sumSquares / uint64(samples))
	}
	stats.NearSilent = stats.Peak <= 32 && stats.RMS <= 4
	return stats, true, nil
}

type pcm16WAVInfo struct {
	pcm        []byte
	durationMS int
}

func parsePCM16WAV(data []byte) (pcm16WAVInfo, bool) {
	if len(data) < 44 || string(data[0:4]) != "RIFF" || string(data[8:12]) != "WAVE" {
		return pcm16WAVInfo{}, false
	}
	var audioFormat uint16
	var channels uint16
	var sampleRate uint32
	var bitsPerSample uint16
	dataStart := -1
	dataSize := 0
	for off := 12; off+8 <= len(data); {
		chunkID := string(data[off : off+4])
		chunkSize := int(binary.LittleEndian.Uint32(data[off+4 : off+8]))
		chunkStart := off + 8
		chunkEnd := chunkStart + chunkSize
		if chunkSize < 0 || chunkEnd > len(data) {
			return pcm16WAVInfo{}, false
		}
		switch chunkID {
		case "fmt ":
			if chunkSize >= 16 {
				audioFormat = binary.LittleEndian.Uint16(data[chunkStart : chunkStart+2])
				channels = binary.LittleEndian.Uint16(data[chunkStart+2 : chunkStart+4])
				sampleRate = binary.LittleEndian.Uint32(data[chunkStart+4 : chunkStart+8])
				bitsPerSample = binary.LittleEndian.Uint16(data[chunkStart+14 : chunkStart+16])
			}
		case "data":
			dataStart = chunkStart
			dataSize = chunkSize
		}
		off = chunkEnd
		if off%2 == 1 {
			off++
		}
	}
	if audioFormat != 1 || bitsPerSample != 16 || channels == 0 || sampleRate == 0 || dataStart < 0 || dataSize < 2 {
		return pcm16WAVInfo{}, false
	}
	durationMS := int((uint64(dataSize) * 1000) / (uint64(sampleRate) * uint64(channels) * 2))
	return pcm16WAVInfo{pcm: data[dataStart : dataStart+dataSize], durationMS: durationMS}, true
}

func intSqrt(n uint64) int {
	if n == 0 {
		return 0
	}
	x := n
	y := (x + 1) / 2
	for y < x {
		x = y
		y = (x + n/x) / 2
	}
	return int(x)
}
