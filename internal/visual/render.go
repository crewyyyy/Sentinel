package visual

import (
	"bytes"
	"context"
	"encoding/binary"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"time"
)

const MaxFrames = 3
const MaxInputBytes = 20 << 20

type Renderer interface {
	Frames(context.Context, []byte, int) ([][]byte, error)
}
type FFmpeg struct{ Binary string }

// Frames samples animations, never executes their contents or resolves referenced URLs.
func (r FFmpeg) Frames(ctx context.Context, data []byte, duration int) ([][]byte, error) {
	if len(data) < 12 || len(data) > MaxInputBytes {
		return nil, &Error{Code: "media_file_limit", Permanent: true}
	}
	format := ""
	animated := false
	switch {
	case bytes.HasPrefix(data, []byte("\x89PNG\r\n\x1a\n")):
		format = "png_pipe"
	case bytes.HasPrefix(data, []byte{0xff, 0xd8, 0xff}):
		format = "jpeg_pipe"
	case string(data[:4]) == "RIFF" && string(data[8:12]) == "WEBP":
		format = "webp_pipe"
	case string(data[:6]) == "GIF89a" || string(data[:6]) == "GIF87a":
		format = "gif"
		animated = true
	case string(data[4:8]) == "ftyp":
		format = "mov"
		animated = true
	case bytes.HasPrefix(data, []byte{0x1a, 0x45, 0xdf, 0xa3}):
		format = "matroska"
		animated = true
	default:
		return nil, &Error{Code: "media_format_unsupported", Permanent: true}
	}
	dir, err := os.MkdirTemp("", "sentinel-media-")
	if err != nil {
		return nil, &Error{Code: "media_storage_unavailable"}
	}
	defer os.RemoveAll(dir)
	input := filepath.Join(dir, "input")
	if err = os.WriteFile(input, data, 0600); err != nil {
		return nil, &Error{Code: "media_storage_unavailable"}
	}
	name := r.Binary
	if name == "" {
		name = "ffmpeg"
	}
	limitCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	filter := "scale=768:768:force_original_aspect_ratio=decrease"
	frames := 1
	if animated {
		frames = MaxFrames
		// Telegram duration is bounded metadata, not an input to a shell command.
		seconds := min(60, max(1, duration))
		filter = "fps=" + strconv.Itoa(MaxFrames) + "/" + strconv.Itoa(seconds) + "," + filter
	}
	args := []string{"-nostdin", "-v", "error", "-threads", "1", "-filter_threads", "1", "-protocol_whitelist", "file,pipe", "-max_pixels", "16777216", "-f", format, "-i", input, "-t", "60", "-an", "-sn", "-dn", "-vf", filter, "-frames:v", strconv.Itoa(frames), "-c:v", "png", "-f", "image2pipe", "pipe:1"}
	cmd := exec.CommandContext(limitCtx, name, args...)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, &Error{Code: "media_render_unavailable"}
	}
	// Decoder diagnostics may contain untrusted metadata; never include them in errors/logs.
	cmd.Stderr = io.Discard
	if err = cmd.Start(); err != nil {
		return nil, &Error{Code: "media_render_unavailable"}
	}
	output, readErr := io.ReadAll(io.LimitReader(stdout, MaxFrames*MaxFrameBytes+1))
	if readErr != nil || len(output) > MaxFrames*MaxFrameBytes {
		cancel()
	}
	waitErr := cmd.Wait()
	if readErr != nil || waitErr != nil || len(output) > MaxFrames*MaxFrameBytes {
		return nil, &Error{Code: "media_render_failed", Permanent: limitCtx.Err() == nil}
	}
	result, err := splitPNG(output)
	if err != nil {
		return nil, err
	}
	return result, nil
}

func splitPNG(data []byte) ([][]byte, error) {
	frames := [][]byte{}
	for len(data) > 0 && len(frames) < MaxFrames {
		if len(data) < 8 || !bytes.Equal(data[:8], []byte("\x89PNG\r\n\x1a\n")) {
			return nil, &Error{Code: "media_render_invalid", Permanent: true}
		}
		pos := 8
		ended := false
		for pos+12 <= len(data) {
			n := int(binary.BigEndian.Uint32(data[pos : pos+4]))
			if n > MaxFrameBytes || n+12 > len(data)-pos {
				break
			}
			typ := string(data[pos+4 : pos+8])
			pos += n + 12
			if pos > MaxFrameBytes {
				break
			}
			if typ == "IEND" {
				ended = true
				break
			}
		}
		if !ended || pos > MaxFrameBytes {
			return nil, &Error{Code: "media_render_invalid", Permanent: true}
		}
		frames = append(frames, append([]byte(nil), data[:pos]...))
		data = data[pos:]
	}
	if len(frames) == 0 || len(data) > 0 {
		return nil, &Error{Code: "media_render_invalid", Permanent: true}
	}
	return frames, nil
}
