//go:build windows

package audiorouter

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"runtime"
	"time"
	"unsafe"

	"github.com/go-ole/go-ole"
	"github.com/moutend/go-wca/pkg/wca"
)

type WASAPIPlayer struct{}

func NewWASAPIPlayer() *WASAPIPlayer {
	return &WASAPIPlayer{}
}

func (p *WASAPIPlayer) ListDevices(ctx context.Context) ([]Device, error) {
	_ = ctx
	return withCOM(func() ([]Device, error) {
		return enumerateRenderDevices()
	})
}

func (p *WASAPIPlayer) PlayWAV(ctx context.Context, deviceID string, wavData []byte, buffer time.Duration) error {
	decoded, err := decodeWAV(wavData)
	if err != nil {
		return err
	}
	_, err = withCOM(func() (struct{}, error) {
		device, err := findRenderDevice(deviceID)
		if err != nil {
			return struct{}{}, err
		}
		defer device.Release()

		var audioClient *wca.IAudioClient
		if err := device.Activate(wca.IID_IAudioClient, wca.CLSCTX_ALL, nil, &audioClient); err != nil {
			return struct{}{}, fmt.Errorf("activate IAudioClient: %w", err)
		}
		defer audioClient.Release()

		streamFlags := uint32(wca.AUDCLNT_STREAMFLAGS_AUTOCONVERTPCM | wca.AUDCLNT_STREAMFLAGS_SRC_DEFAULT_QUALITY)
		refTime := wca.REFERENCE_TIME(buffer.Nanoseconds() / 100)
		if err := audioClient.Initialize(wca.AUDCLNT_SHAREMODE_SHARED, streamFlags, refTime, 0, &decoded.Format, nil); err != nil {
			return struct{}{}, fmt.Errorf("initialize audio client: %w", err)
		}

		var bufferFrames uint32
		if err := audioClient.GetBufferSize(&bufferFrames); err != nil {
			return struct{}{}, fmt.Errorf("get buffer size: %w", err)
		}

		var renderClient *wca.IAudioRenderClient
		if err := audioClient.GetService(wca.IID_IAudioRenderClient, &renderClient); err != nil {
			return struct{}{}, fmt.Errorf("get render service: %w", err)
		}
		defer renderClient.Release()

		if err := audioClient.Start(); err != nil {
			return struct{}{}, fmt.Errorf("start audio client: %w", err)
		}
		defer audioClient.Stop()

		bytesPerFrame := int(decoded.Format.NBlockAlign)
		totalFrames := len(decoded.Data) / bytesPerFrame
		offsetFrames := 0

		for offsetFrames < totalFrames {
			select {
			case <-ctx.Done():
				return struct{}{}, ctx.Err()
			default:
			}

			var padding uint32
			if err := audioClient.GetCurrentPadding(&padding); err != nil {
				return struct{}{}, fmt.Errorf("get current padding: %w", err)
			}
			available := int(bufferFrames - padding)
			if available <= 0 {
				time.Sleep(5 * time.Millisecond)
				continue
			}

			frames := totalFrames - offsetFrames
			if frames > available {
				frames = available
			}

			var buf *byte
			if err := renderClient.GetBuffer(uint32(frames), &buf); err != nil {
				return struct{}{}, fmt.Errorf("render get buffer: %w", err)
			}
			size := frames * bytesPerFrame
			srcStart := offsetFrames * bytesPerFrame
			copy(unsafe.Slice(buf, size), decoded.Data[srcStart:srcStart+size])
			if err := renderClient.ReleaseBuffer(uint32(frames), 0); err != nil {
				return struct{}{}, fmt.Errorf("render release buffer: %w", err)
			}
			offsetFrames += frames
		}

		for {
			select {
			case <-ctx.Done():
				return struct{}{}, ctx.Err()
			default:
			}
			var padding uint32
			if err := audioClient.GetCurrentPadding(&padding); err != nil {
				return struct{}{}, fmt.Errorf("drain padding: %w", err)
			}
			if padding == 0 {
				return struct{}{}, nil
			}
			time.Sleep(5 * time.Millisecond)
		}
	})
	return err
}

func withCOM[T any](fn func() (T, error)) (T, error) {
	var zero T
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	if err := ole.CoInitializeEx(0, ole.COINIT_MULTITHREADED); err != nil {
		return zero, err
	}
	defer ole.CoUninitialize()
	return fn()
}

func enumerateRenderDevices() ([]Device, error) {
	enumerator, err := newDeviceEnumerator()
	if err != nil {
		return nil, err
	}
	defer enumerator.Release()

	var collection *wca.IMMDeviceCollection
	if err := enumerator.EnumAudioEndpoints(wca.ERender, wca.DEVICE_STATE_ACTIVE, &collection); err != nil {
		return nil, fmt.Errorf("enum audio endpoints: %w", err)
	}
	defer collection.Release()

	var count uint32
	if err := collection.GetCount(&count); err != nil {
		return nil, fmt.Errorf("get device count: %w", err)
	}
	out := make([]Device, 0, count)
	for i := uint32(0); i < count; i++ {
		var dev *wca.IMMDevice
		if err := collection.Item(i, &dev); err != nil {
			return nil, fmt.Errorf("get device item %d: %w", i, err)
		}
		id, name, err := readDeviceIdentity(dev)
		dev.Release()
		if err != nil {
			return nil, err
		}
		out = append(out, Device{ID: id, Name: name})
	}
	return out, nil
}

func findRenderDevice(deviceID string) (*wca.IMMDevice, error) {
	enumerator, err := newDeviceEnumerator()
	if err != nil {
		return nil, err
	}
	defer enumerator.Release()

	var collection *wca.IMMDeviceCollection
	if err := enumerator.EnumAudioEndpoints(wca.ERender, wca.DEVICE_STATE_ACTIVE, &collection); err != nil {
		return nil, fmt.Errorf("enum audio endpoints: %w", err)
	}
	defer collection.Release()

	var count uint32
	if err := collection.GetCount(&count); err != nil {
		return nil, fmt.Errorf("get device count: %w", err)
	}
	for i := uint32(0); i < count; i++ {
		var dev *wca.IMMDevice
		if err := collection.Item(i, &dev); err != nil {
			return nil, fmt.Errorf("get device item %d: %w", i, err)
		}
		id, _, err := readDeviceIdentity(dev)
		if err != nil {
			dev.Release()
			return nil, err
		}
		if id == deviceID {
			return dev, nil
		}
		dev.Release()
	}
	return nil, fmt.Errorf("device not found: %s", deviceID)
}

func newDeviceEnumerator() (*wca.IMMDeviceEnumerator, error) {
	var enumerator *wca.IMMDeviceEnumerator
	if err := wca.CoCreateInstance(wca.CLSID_MMDeviceEnumerator, 0, wca.CLSCTX_ALL, wca.IID_IMMDeviceEnumerator, &enumerator); err != nil {
		return nil, fmt.Errorf("create MMDeviceEnumerator: %w", err)
	}
	return enumerator, nil
}

func readDeviceIdentity(dev *wca.IMMDevice) (string, string, error) {
	var id string
	if err := dev.GetId(&id); err != nil {
		return "", "", fmt.Errorf("get device id: %w", err)
	}
	var store *wca.IPropertyStore
	if err := dev.OpenPropertyStore(wca.STGM_READ, &store); err != nil {
		return "", "", fmt.Errorf("open property store: %w", err)
	}
	defer store.Release()

	var value wca.PROPVARIANT
	if err := store.GetValue(&wca.PKEY_Device_FriendlyName, &value); err != nil {
		return "", "", fmt.Errorf("get friendly name: %w", err)
	}
	defer value.Clear()
	return id, value.String(), nil
}

type decodedWAV struct {
	Format wca.WAVEFORMATEX
	Data   []byte
}

func decodeWAV(data []byte) (*decodedWAV, error) {
	if len(data) < 12 || string(data[:4]) != "RIFF" || string(data[8:12]) != "WAVE" {
		return nil, fmt.Errorf("invalid wav header")
	}
	r := bytes.NewReader(data[12:])
	var format wca.WAVEFORMATEX
	var pcmData []byte
	for r.Len() > 0 {
		var chunkID [4]byte
		if err := binary.Read(r, binary.LittleEndian, &chunkID); err != nil {
			break
		}
		var chunkSize uint32
		if err := binary.Read(r, binary.LittleEndian, &chunkSize); err != nil {
			return nil, fmt.Errorf("read chunk size: %w", err)
		}
		chunk := make([]byte, chunkSize)
		if _, err := r.Read(chunk); err != nil {
			return nil, fmt.Errorf("read chunk data: %w", err)
		}
		switch string(chunkID[:]) {
		case "fmt ":
			if chunkSize < 16 {
				return nil, fmt.Errorf("fmt chunk too short")
			}
			format = wca.WAVEFORMATEX{
				WFormatTag:      binary.LittleEndian.Uint16(chunk[0:2]),
				NChannels:       binary.LittleEndian.Uint16(chunk[2:4]),
				NSamplesPerSec:  binary.LittleEndian.Uint32(chunk[4:8]),
				NAvgBytesPerSec: binary.LittleEndian.Uint32(chunk[8:12]),
				NBlockAlign:     binary.LittleEndian.Uint16(chunk[12:14]),
				WBitsPerSample:  binary.LittleEndian.Uint16(chunk[14:16]),
			}
			if chunkSize >= 18 {
				format.CbSize = binary.LittleEndian.Uint16(chunk[16:18])
			}
		case "data":
			pcmData = append([]byte(nil), chunk...)
		}
		if chunkSize%2 == 1 {
			_, _ = r.ReadByte()
		}
	}
	if format.WFormatTag == 0 || len(pcmData) == 0 {
		return nil, fmt.Errorf("wav missing fmt/data chunks")
	}
	if format.NBlockAlign == 0 {
		return nil, fmt.Errorf("wav has invalid block alignment")
	}
	return &decodedWAV{Format: format, Data: pcmData}, nil
}
