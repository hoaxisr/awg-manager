package geodata

type GeoFileEntry struct {
	Type     string `json:"type"`
	Path     string `json:"path"`
	URL      string `json:"url"`
	Size     int64  `json:"size"`
	TagCount int    `json:"tagCount"`
	Updated  string `json:"updated"`
	// External is true when the .dat file lives under /opt/etc/HydraRoute:
	// another program owns it, awg-manager only reads it.
	External bool `json:"external,omitempty"`
	// Mtime (RFC3339 UTC) is the file's modification time at the moment
	// TagCount was computed. Used to detect stale cached TagCount without
	// re-parsing the file: stat() returns the same mtime+size → cache hit.
	Mtime string `json:"mtime,omitempty"`
}

type GeoTag struct {
	Name  string `json:"name"`
	Count int    `json:"count"`
}

const maxGeoFiles = 16

// hrDir — каталог HydraRoute Neo: признак чужого (External) файла. var, чтобы
// тесты подменяли его через t.TempDir().
var hrDir = "/opt/etc/HydraRoute" //nolint:gochecknoglobals
