package main

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/klauspost/compress/zstd"
)

type ReleaseAsset struct {
	Name               string `json:"name"`
	BrowserDownloadURL string `json:"browser_download_url"`
}

type Release struct {
	TagName string         `json:"tag_name"`
	Assets  []ReleaseAsset `json:"assets"`
}

func fetchLatestRelease(repo string) (*Release, error) {
	url := fmt.Sprintf("https://api.github.com/repos/%s/releases/latest", repo)
	resp, err := http.Get(url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GitHub API 请求失败: %s", resp.Status)
	}

	var release Release
	if err := json.NewDecoder(resp.Body).Decode(&release); err != nil {
		return nil, err
	}
	return &release, nil
}

func downloadFile(url string) ([]byte, error) {
	resp, err := http.Get(url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	return io.ReadAll(resp.Body)
}

func compressZstd(data []byte) ([]byte, error) {
	encoder, err := zstd.NewWriter(nil, zstd.WithEncoderLevel(zstd.SpeedDefault))
	if err != nil {
		return nil, err
	}
	return encoder.EncodeAll(data, make([]byte, 0, len(data))), nil
}

func fileHash(path string) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return nil, err
	}
	return h.Sum(nil), nil
}

func runGitCommands(relPath string, tag string, push bool, component string) error {
	commitMsg := fmt.Sprintf("chore(%s): update to %s", component, tag)
	cmds := []struct {
		args []string
		desc string
	}{
		{[]string{"git", "add", relPath}, "git 添加"},
		{[]string{"git", "commit", "-m", commitMsg}, "git 提交"},
	}
	if push {
		cmds = append(cmds, struct {
			args []string
			desc string
		}{[]string{"git", "push", "origin", "main"}, "git 推送"})
	}

	for _, cmd := range cmds {
		out, err := exec.Command(cmd.args[0], cmd.args[1:]...).CombinedOutput()
		if err != nil {
			return fmt.Errorf("%s 失败: %v\n输出: %s", cmd.desc, err, out)
		}
	}

	log.Printf("成功更新 %s 到 %s", component, tag)
	if push {
		log.Println("已完成 git 提交和远程仓库推送")
	} else {
		log.Println("已完成 git 提交, 请手动推送到远程仓库")
	}
	return nil
}

func updateBackend(destDir, gitDir string, push bool) {
	release, err := fetchLatestRelease("sub-store-org/Sub-Store")
	if err != nil {
		log.Fatalf("获取后端 release 失败: %v", err)
	}

	var downloadURL string
	for _, asset := range release.Assets {
		if asset.Name == "sub-store.bundle.js" {
			downloadURL = asset.BrowserDownloadURL
			break
		}
	}
	if downloadURL == "" {
		log.Fatal("未找到 sub-store.bundle.js")
	}

	log.Println("后端最新版本:", release.TagName)
	log.Println("下载地址:", downloadURL)

	jsData, err := downloadFile(downloadURL)
	if err != nil {
		log.Fatalf("下载后端文件失败: %v", err)
	}

	compressed, err := compressZstd(jsData)
	if err != nil {
		log.Fatalf("压缩后端文件失败: %v", err)
	}

	destPath := filepath.Join(destDir, "sub-store.bundle.js.zst")
	currentHash, err := fileHash(destPath)
	if err != nil && !os.IsNotExist(err) {
		log.Printf("无法计算当前后端文件哈希: %v", err)
	}

	newHash := sha256.Sum256(compressed)

	if bytes.Equal(currentHash, newHash[:]) {
		log.Println("后端文件已是最新，无需更新。")
		return
	}

	log.Println("后端文件有更新，准备替换...")
	if err := os.WriteFile(destPath, compressed, 0644); err != nil {
		log.Fatalf("写入后端文件失败: %v", err)
	}
	log.Println("已将后端压缩文件更新到:", destPath)

	originalWd, _ := os.Getwd()
	if err := os.Chdir(gitDir); err != nil {
		log.Fatalf("切换到 git 目录失败: %v", err)
	}
	defer os.Chdir(originalWd)

	relPath, _ := filepath.Rel(gitDir, destPath)
	if err := runGitCommands(relPath, release.TagName, push, "sub-store"); err != nil {
		log.Fatalf("后端 git 操作失败: %v", err)
	}
}

func updateFrontend(destDir, gitDir string, push bool) {
	release, err := fetchLatestRelease("sub-store-org/Sub-Store-Front-End")
	if err != nil {
		log.Fatalf("获取前端 release 失败: %v", err)
	}

	var downloadURL string
	for _, asset := range release.Assets {
		if asset.Name == "dist.zip" {
			downloadURL = asset.BrowserDownloadURL
			break
		}
	}
	if downloadURL == "" {
		log.Fatal("未找到 dist.zip")
	}

	log.Println("前端最新版本:", release.TagName)
	log.Println("下载地址:", downloadURL)

	zipData, err := downloadFile(downloadURL)
	if err != nil {
		log.Fatalf("下载前端文件失败: %v", err)
	}

	tmpDir := "dist_temp"
	os.RemoveAll(tmpDir)
	defer os.RemoveAll(tmpDir)

	zipReader, err := zip.NewReader(bytes.NewReader(zipData), int64(len(zipData)))
	if err != nil {
		log.Fatalf("创建 zip reader 失败: %v", err)
	}

	for _, f := range zipReader.File {
		fpath := filepath.Join(tmpDir, f.Name)
		if !strings.HasPrefix(fpath, filepath.Clean(tmpDir)+string(os.PathSeparator)) {
			log.Fatalf("非法文件路径: %s", fpath)
		}
		if f.FileInfo().IsDir() {
			os.MkdirAll(fpath, os.ModePerm)
			continue
		}
		if err = os.MkdirAll(filepath.Dir(fpath), os.ModePerm); err != nil {
			log.Fatalf("创建目录失败: %v", err)
		}
		outFile, err := os.OpenFile(fpath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, f.Mode())
		if err != nil {
			log.Fatalf("创建文件失败: %v", err)
		}
		rc, err := f.Open()
		if err != nil {
			outFile.Close()
			log.Fatalf("打开 zip 内文件失败: %v", err)
		}
		_, err = io.Copy(outFile, rc)
		outFile.Close()
		rc.Close()
		if err != nil {
			log.Fatalf("解压文件失败: %v", err)
		}
	}

	var tarZstBuf bytes.Buffer
	zstdEncoder, err := zstd.NewWriter(&tarZstBuf, zstd.WithEncoderLevel(zstd.SpeedDefault))
	if err != nil {
		log.Fatalf("创建 zstd writer 失败: %v", err)
	}
	tw := tar.NewWriter(zstdEncoder)
	srcDir := filepath.Join(tmpDir, "dist")

	filepath.Walk(srcDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		relPath, err := filepath.Rel(srcDir, path)
		if err != nil {
			return err
		}
		relPath = filepath.ToSlash(filepath.Join("frontend", relPath))

		hdr, err := tar.FileInfoHeader(info, relPath)
		if err != nil {
			return err
		}
		hdr.Name = relPath
		if err := tw.WriteHeader(hdr); err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return nil
		}
		f, err := os.Open(path)
		if err != nil {
			return err
		}
		defer f.Close()
		_, err = io.Copy(tw, f)
		return err
	})
	tw.Close()
	zstdEncoder.Close()

	tarData := tarZstBuf.Bytes()
	destPath := filepath.Join(destDir, "sub-store.frontend.tar.zst")

	currentHash, err := fileHash(destPath)
	if err != nil && !os.IsNotExist(err) {
		log.Printf("无法计算当前前端文件哈希: %v", err)
	}

	newHash := sha256.Sum256(tarData)

	if bytes.Equal(currentHash, newHash[:]) {
		log.Println("前端文件已是最新，无需更新。")
		return
	}

	log.Println("前端文件有更新，准备替换...")
	if err := os.WriteFile(destPath, tarData, 0644); err != nil {
		log.Fatalf("写入前端文件失败: %v", err)
	}
	log.Println("已将前端 tar 文件更新到:", destPath)

	originalWd, _ := os.Getwd()
	if err := os.Chdir(gitDir); err != nil {
		log.Fatalf("切换到 git 目录失败: %v", err)
	}
	defer os.Chdir(originalWd)

	relPath, _ := filepath.Rel(gitDir, destPath)
	if err := runGitCommands(relPath, release.TagName, push, "sub-store-frontend"); err != nil {
		log.Fatalf("前端 git 操作失败: %v", err)
	}
}

func main() {
	push := false
	for _, arg := range os.Args[1:] {
		if arg == "--push" || arg == "-p" {
			push = true
			break
		}
	}

	commonProxies := []string{
		"http://127.0.0.1:7890",
		"http://127.0.0.1:7891",
		"http://127.0.0.1:1080",
		"http://127.0.0.1:8080",
		"http://127.0.0.1:10808",
		"http://127.0.0.1:10809",
	}

	proxy := findAvailableProxy("http://127.0.0.1:10808", commonProxies)
	if proxy != "" {
		os.Setenv("HTTP_PROXY", proxy)
		os.Setenv("HTTPS_PROXY", proxy)
		log.Println("使用代理:", proxy)
	} else {
		log.Println("未找到可用代理，将不设置代理")
	}

	destDir := `d:\Desktop\GoWork\subs-check-pro\assets`
	gitDir := filepath.Dir(destDir)

	if err := os.MkdirAll(destDir, 0755); err != nil {
		log.Fatalf("创建目标目录失败: %v", err)
	}

	updateBackend(destDir, gitDir, push)
	updateFrontend(destDir, gitDir, push)

	log.Println("--- 所有检查已完成 ---")
}
