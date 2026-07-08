package types

// Coords is a block position in the world.
type Coords struct {
	X int `json:"x"`
	Y int `json:"y"`
	Z int `json:"z"`
}

// WorldInfo describes the active world. Spawn is omitted when level.dat can't
// be read (e.g. the world hasn't been generated yet).
type WorldInfo struct {
	LevelName string  `json:"level_name"`
	Spawn     *Coords `json:"spawn,omitempty"`
}
