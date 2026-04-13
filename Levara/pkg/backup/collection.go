package backup

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strings"
)

// ExportCollection exports a single collection via Levara API.
func ExportCollection(serverURL, collection, output string) error {
	url := fmt.Sprintf("%s/api/v1/sync/export/collection/%s", strings.TrimSuffix(serverURL, "/"), collection)
	log.Printf("[export] fetching collection %q from %s", collection, url)

	resp, err := http.Get(url)
	if err != nil {
		return fmt.Errorf("GET %s: %w", url, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("server returned %d: %s", resp.StatusCode, string(body))
	}

	f, err := os.Create(output)
	if err != nil {
		return fmt.Errorf("create %s: %w", output, err)
	}
	defer f.Close()

	n, err := io.Copy(f, resp.Body)
	if err != nil {
		return fmt.Errorf("write: %w", err)
	}

	log.Printf("[export] collection %q saved to %s (%d bytes)", collection, output, n)
	return nil
}

// ImportCollection imports a collection via Levara API (async, re-embeds).
func ImportCollection(serverURL, input string) error {
	data, err := os.ReadFile(input)
	if err != nil {
		return fmt.Errorf("read %s: %w", input, err)
	}

	url := fmt.Sprintf("%s/api/v1/sync/import/collection", strings.TrimSuffix(serverURL, "/"))
	log.Printf("[import] importing collection from %s to %s", input, url)

	resp, err := http.Post(url, "application/json", strings.NewReader(string(data)))
	if err != nil {
		return fmt.Errorf("POST %s: %w", url, err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 && resp.StatusCode != 201 {
		return fmt.Errorf("server returned %d: %s", resp.StatusCode, string(body))
	}

	var result map[string]interface{}
	json.Unmarshal(body, &result)
	log.Printf("[import] started: %v", result)
	return nil
}

// ListData shows what's in a Levara instance.
func ListData(serverURL string) error {
	// Collections
	colls := fetchJSON(serverURL + "/api/v1/collections")
	if arr, ok := colls.([]interface{}); ok {
		fmt.Printf("Collections (%d):\n", len(arr))
		for _, c := range arr {
			m := c.(map[string]interface{})
			fmt.Printf("  %-30s %6.0f records  dim=%v\n", m["name"], m["record_count"], m["embedding_dim"])
		}
	}

	// Datasets
	datasets := fetchJSON(serverURL + "/api/v1/datasets")
	if arr, ok := datasets.([]interface{}); ok {
		fmt.Printf("\nDatasets (%d):\n", len(arr))
		for _, d := range arr {
			m := d.(map[string]interface{})
			fmt.Printf("  %-30s %6.0f records\n", m["name"], m["record_count"])
		}
	}

	// Info
	info := fetchJSON(serverURL + "/api/v1/info")
	if m, ok := info.(map[string]interface{}); ok {
		fmt.Printf("\nServer: status=%v dim=%v shards=%v\n", m["status"], m["dimension"], m["shards"])
	}

	return nil
}

func fetchJSON(url string) interface{} {
	resp, err := http.Get(url)
	if err != nil {
		return nil
	}
	defer resp.Body.Close()
	var result interface{}
	json.NewDecoder(resp.Body).Decode(&result)
	return result
}
