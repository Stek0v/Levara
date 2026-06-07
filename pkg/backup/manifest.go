package backup

import (
	"encoding/json"
	"os"
	"time"
)

type Manifest struct {
	Version      string   `json:"version"`
	CreatedAt    string   `json:"created_at"`
	DataDir      string   `json:"data_dir"`
	DBProvider   string   `json:"db_provider"`
	Collections  []string `json:"collections"`
	Datasets     int      `json:"datasets"`
	Memories     int      `json:"memories"`
	Interactions int      `json:"interactions"`
	UploadsCount int      `json:"uploads_count"`
	UploadsSizeB int64    `json:"uploads_size_bytes"`
}

func NewManifest(dataDir, dbProvider string) Manifest {
	return Manifest{
		Version:   "1.0",
		CreatedAt: time.Now().UTC().Format(time.RFC3339),
		DataDir:   dataDir,
		DBProvider: dbProvider,
	}
}

func (m Manifest) Write(path string) error {
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0644)
}

func ReadManifest(path string) (Manifest, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Manifest{}, err
	}
	var m Manifest
	err = json.Unmarshal(data, &m)
	return m, err
}
