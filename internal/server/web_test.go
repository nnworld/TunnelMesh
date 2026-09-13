package server

import (
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
)

func TestEmbeddedWebDistContainsAgentMetadataUI(t *testing.T) {
	var bundle strings.Builder
	err := fs.WalkDir(webDist, "web_dist/assets", func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".js") {
			return nil
		}
		data, err := fs.ReadFile(webDist, path)
		if err != nil {
			return err
		}
		bundle.Write(data)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	content := bundle.String()
	for _, keyword := range []string{"Agent details", "Redacted", "includeStale", "Access policies", "Delete access policy", "Access policy restored", "restore-agent-policy"} {
		if !strings.Contains(content, keyword) {
			t.Fatalf("embedded web bundle does not contain %q", keyword)
		}
	}
}

func TestStaticFileServerPinsWasmContentType(t *testing.T) {
	files := fstest.MapFS{
		"assets/app.wasm": &fstest.MapFile{Data: []byte("\x00asm\x01\x00\x00\x00")},
		"assets/app.js":   &fstest.MapFile{Data: []byte("console.log(1)\n")},
	}
	handler := staticFileServer(files)

	wasm := httptest.NewRecorder()
	handler.ServeHTTP(wasm, httptest.NewRequest(http.MethodGet, "/assets/app.wasm", nil))
	if got := wasm.Header().Get("Content-Type"); got != "application/wasm" {
		t.Fatalf("wasm content type = %q, want application/wasm", got)
	}

	script := httptest.NewRecorder()
	handler.ServeHTTP(script, httptest.NewRequest(http.MethodGet, "/assets/app.js", nil))
	if got := script.Header().Get("Content-Type"); !strings.Contains(got, "javascript") {
		t.Fatalf("js content type = %q, want a javascript media type", got)
	}
}

// //go:embed 默认跳过以 _ 或 . 开头的文件，而 Vite 会产出 _baseClone-*.js 这类
// lodash 辅助 chunk。漏掉后浏览器请求会落到 SPA history fallback 拿到 index.html，
// ES module 加载失败，引用它的路由视图整页挂载失败（白屏）。这里把内嵌 FS 与源目录
// 做全量比对，任何漏嵌文件都必须让测试失败。
func TestEmbeddedWebDistContainsEverySourceFile(t *testing.T) {
	embedded := make(map[string]bool)
	err := fs.WalkDir(webDist, "web_dist", func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !entry.IsDir() {
			embedded[strings.TrimPrefix(filepath.ToSlash(path), "web_dist/")] = true
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(embedded) == 0 {
		t.Fatal("embedded web_dist is empty")
	}

	var missing []string
	err = filepath.WalkDir("web_dist", func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		rel, relErr := filepath.Rel("web_dist", path)
		if relErr != nil {
			return relErr
		}
		if rel = filepath.ToSlash(rel); !embedded[rel] {
			missing = append(missing, rel)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(missing) > 0 {
		t.Fatalf("embedded web_dist is missing %d source file(s): %v", len(missing), missing)
	}
	if _, err := os.Stat("web_dist/index.html"); err != nil {
		t.Fatalf("web_dist source directory is not readable: %v", err)
	}
}
