package services

import (
	"compress/gzip"
	"encoding/binary"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/lomokwa/mc-manager/types"
)

// NBT tag type IDs (see https://minecraft.wiki/w/NBT_format).
const (
	tagEnd byte = iota
	tagByte
	tagShort
	tagInt
	tagLong
	tagFloat
	tagDouble
	tagByteArray
	tagString
	tagList
	tagCompound
	tagIntArray
	tagLongArray
)

const maxLevelDatSize = 8 << 20 // 8 MiB, guards against a decompression bomb

// GetWorldInfo returns the active world's name and spawn coordinates. Spawn is
// nil when level.dat is missing or unreadable.
func GetWorldInfo() types.WorldInfo {
	level := getLevelName()
	info := types.WorldInfo{LevelName: level}
	if x, y, z, ok := readWorldSpawn(filepath.Join(ServerDir, level)); ok {
		info.Spawn = &types.Coords{X: x, Y: y, Z: z}
	}
	return info
}

// readWorldSpawn reads the spawn coordinates from <worldDir>/level.dat, which
// is gzip-compressed NBT.
func readWorldSpawn(worldDir string) (x, y, z int, ok bool) {
	f, err := os.Open(filepath.Join(worldDir, "level.dat"))
	if err != nil {
		return 0, 0, 0, false
	}
	defer f.Close()

	gz, err := gzip.NewReader(f)
	if err != nil {
		return 0, 0, 0, false
	}
	defer gz.Close()

	data, err := io.ReadAll(io.LimitReader(gz, maxLevelDatSize))
	if err != nil {
		return 0, 0, 0, false
	}
	return parseNBTSpawn(data)
}

// parseNBTSpawn walks the root compound to Data.SpawnX/Y/Z.
func parseNBTSpawn(data []byte) (x, y, z int, ok bool) {
	d := &nbtDecoder{b: data}
	tag, err := d.u8()
	if err != nil || tag != tagCompound {
		return 0, 0, 0, false
	}
	if _, err := d.name(); err != nil { // root name, conventionally ""
		return 0, 0, 0, false
	}
	root, err := d.value(tagCompound, 0)
	if err != nil {
		return 0, 0, 0, false
	}

	data0, _ := root.(map[string]any)["Data"].(map[string]any)
	if data0 == nil {
		return 0, 0, 0, false
	}
	sx, ok1 := data0["SpawnX"].(int32)
	sy, ok2 := data0["SpawnY"].(int32)
	sz, ok3 := data0["SpawnZ"].(int32)
	if !ok1 || !ok2 || !ok3 {
		return 0, 0, 0, false
	}
	return int(sx), int(sy), int(sz), true
}

// nbtDecoder reads big-endian NBT from an in-memory buffer. It only retains the
// int and compound values needed to locate the spawn; other payloads are
// consumed (to stay in sync) but discarded.
type nbtDecoder struct {
	b []byte
	p int
}

func (d *nbtDecoder) take(n int) ([]byte, error) {
	if n < 0 || d.p+n > len(d.b) {
		return nil, io.ErrUnexpectedEOF
	}
	s := d.b[d.p : d.p+n]
	d.p += n
	return s, nil
}

func (d *nbtDecoder) u8() (byte, error) {
	s, err := d.take(1)
	if err != nil {
		return 0, err
	}
	return s[0], nil
}

func (d *nbtDecoder) u16() (uint16, error) {
	s, err := d.take(2)
	if err != nil {
		return 0, err
	}
	return binary.BigEndian.Uint16(s), nil
}

func (d *nbtDecoder) i32() (int32, error) {
	s, err := d.take(4)
	if err != nil {
		return 0, err
	}
	return int32(binary.BigEndian.Uint32(s)), nil
}

func (d *nbtDecoder) name() (string, error) {
	n, err := d.u16()
	if err != nil {
		return "", err
	}
	s, err := d.take(int(n))
	if err != nil {
		return "", err
	}
	return string(s), nil
}

// value parses and consumes a payload of the given tag type. depth guards
// against pathologically nested data.
func (d *nbtDecoder) value(tag byte, depth int) (any, error) {
	if depth > 512 {
		return nil, fmt.Errorf("nbt nesting too deep")
	}
	switch tag {
	case tagByte:
		return d.u8()
	case tagShort:
		_, err := d.take(2)
		return nil, err
	case tagInt:
		return d.i32()
	case tagLong:
		_, err := d.take(8)
		return nil, err
	case tagFloat:
		_, err := d.take(4)
		return nil, err
	case tagDouble:
		_, err := d.take(8)
		return nil, err
	case tagByteArray:
		n, err := d.i32()
		if err != nil {
			return nil, err
		}
		_, err = d.take(int(n))
		return nil, err
	case tagString:
		n, err := d.u16()
		if err != nil {
			return nil, err
		}
		_, err = d.take(int(n))
		return nil, err
	case tagList:
		elem, err := d.u8()
		if err != nil {
			return nil, err
		}
		n, err := d.i32()
		if err != nil {
			return nil, err
		}
		for i := int32(0); i < n; i++ {
			if _, err := d.value(elem, depth+1); err != nil {
				return nil, err
			}
		}
		return nil, nil
	case tagCompound:
		m := map[string]any{}
		for {
			t, err := d.u8()
			if err != nil {
				return nil, err
			}
			if t == tagEnd {
				break
			}
			key, err := d.name()
			if err != nil {
				return nil, err
			}
			v, err := d.value(t, depth+1)
			if err != nil {
				return nil, err
			}
			m[key] = v
		}
		return m, nil
	case tagIntArray:
		n, err := d.i32()
		if err != nil {
			return nil, err
		}
		_, err = d.take(int(n) * 4)
		return nil, err
	case tagLongArray:
		n, err := d.i32()
		if err != nil {
			return nil, err
		}
		_, err = d.take(int(n) * 8)
		return nil, err
	default:
		return nil, fmt.Errorf("unknown nbt tag %d", tag)
	}
}
