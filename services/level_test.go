package services

import (
	"bytes"
	"compress/gzip"
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"
)

// nbtName writes an NBT name (uint16 length + bytes).
func nbtName(b *bytes.Buffer, s string) {
	binary.Write(b, binary.BigEndian, uint16(len(s)))
	b.WriteString(s)
}

func nbtInt(b *bytes.Buffer, name string, v int32) {
	b.WriteByte(tagInt)
	nbtName(b, name)
	binary.Write(b, binary.BigEndian, v)
}

// sampleLevelNBT builds an uncompressed level.dat NBT body with a Data
// compound holding spawn coordinates plus a couple of sibling tags (a string
// and a list) the parser must skip over.
func sampleLevelNBT() []byte {
	var b bytes.Buffer
	b.WriteByte(tagCompound) // root
	nbtName(&b, "")
	b.WriteByte(tagCompound) // Data
	nbtName(&b, "Data")

	// A string sibling before the spawn ints.
	b.WriteByte(tagString)
	nbtName(&b, "LevelName")
	binary.Write(&b, binary.BigEndian, uint16(len("world")))
	b.WriteString("world")

	nbtInt(&b, "SpawnX", 100)
	nbtInt(&b, "SpawnY", 64)
	nbtInt(&b, "SpawnZ", -200)

	// An empty list sibling after them.
	b.WriteByte(tagList)
	nbtName(&b, "ServerBrands")
	b.WriteByte(tagString)
	binary.Write(&b, binary.BigEndian, int32(0))

	b.WriteByte(tagEnd) // end Data
	b.WriteByte(tagEnd) // end root
	return b.Bytes()
}

func TestParseNBTSpawn(t *testing.T) {
	x, y, z, ok := parseNBTSpawn(sampleLevelNBT())
	if !ok {
		t.Fatal("expected ok")
	}
	if x != 100 || y != 64 || z != -200 {
		t.Errorf("spawn = %d,%d,%d, want 100,64,-200", x, y, z)
	}

	if _, _, _, ok := parseNBTSpawn([]byte{0x0a, 0x00, 0x00, 0x00}); ok {
		t.Error("expected ok=false for a compound without Data")
	}
}

func TestReadWorldSpawn(t *testing.T) {
	dir := t.TempDir()
	f, err := os.Create(filepath.Join(dir, "level.dat"))
	if err != nil {
		t.Fatal(err)
	}
	gz := gzip.NewWriter(f)
	if _, err := gz.Write(sampleLevelNBT()); err != nil {
		t.Fatal(err)
	}
	gz.Close()
	f.Close()

	x, y, z, ok := readWorldSpawn(dir)
	if !ok || x != 100 || y != 64 || z != -200 {
		t.Errorf("readWorldSpawn = %d,%d,%d ok=%v, want 100,64,-200 true", x, y, z, ok)
	}

	if _, _, _, ok := readWorldSpawn(t.TempDir()); ok {
		t.Error("expected ok=false when level.dat is missing")
	}
}
