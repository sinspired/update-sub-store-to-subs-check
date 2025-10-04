package main

import (
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

func fetchLatestRelease() (*Release, error) {
    resp, err := http.Get("https://api.github.com/repos/sub-store-org/Sub-Store/releases/latest")
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

func runGitCommands(relPath string, tag string, push bool) error {
    // 定义命令和语义化描述
    cmds := []struct {
        args []string
        desc string
    }{
        {[]string{"git", "add", relPath}, "git 添加"},
        {[]string{"git", "commit", "-m", fmt.Sprintf("chore(sub-store): update to %s", tag)}, "git 提交"},
    }
    if push {
        cmds = append(cmds, struct {
            args []string
            desc string
        }{[]string{"git", "push", "origin", "main"}, "git 推送"})
    }

    for _, cmd := range cmds {
        // log.Println("执行命令：", cmd.desc)
        out, err := exec.Command(cmd.args[0], cmd.args[1:]...).CombinedOutput()
        if err != nil {
            return fmt.Errorf("%s 失败: %v\n输出: %s", cmd.desc, err, out)
        }
        // log.Printf("%s 成功: %s\n", cmd.desc, out)
    }

	log.Printf("成功更新 sub-store 到 %s", tag)

    if push {
        log.Println("已完成 git 提交和远程仓库推送")
    } else {
        log.Println("已完成 git 提交, 请手动推送到远程仓库")
    }
    return nil
}

func main() {
    // 检查是否带有 --push 或 -p 参数
    push := false
    for _, arg := range os.Args[1:] {
        if arg == "--push" || arg == "-p" {
            push = true
            break
        }
    }

		// 从配置文件中读取代理，优先使用配置文件代理，不可用则自动检测常见端口
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

    release, err := fetchLatestRelease()
    if err != nil {
        log.Fatalf("获取 release 失败: %v", err)
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

    log.Println("最新版本:", release.TagName)
    log.Println("下载地址:", downloadURL)

    jsData, err := downloadFile(downloadURL)
    if err != nil {
        log.Fatalf("下载文件失败: %v", err)
    }

    compressed, err := compressZstd(jsData)
    if err != nil {
        log.Fatalf("压缩失败: %v", err)
    }

    jsPath := "sub-store.bundle.js"
    zstPath := "sub-store.bundle.js.zst"
    if err := os.WriteFile(jsPath, jsData, 0644); err != nil {
        log.Fatalf("保存 js 文件失败: %v", err)
    }
    if err := os.WriteFile(zstPath, compressed, 0644); err != nil {
        log.Fatalf("保存 zst 文件失败: %v", err)
    }
    log.Println("成功保存:", jsPath, "和", zstPath)

    destDir := `d:\Desktop\GoWork\subs-check\assets`
    destPath := filepath.Join(destDir, "sub-store.bundle.js.zst")

    zstHash, _ := fileHash(zstPath)
    destHash, _ := fileHash(destPath)

    if destHash == nil || !bytes.Equal(zstHash, destHash) {
        log.Println("目标文件不存在或哈希不匹配，准备替换...")
        if err := os.MkdirAll(destDir, 0755); err != nil {
            log.Fatalf("创建目录失败: %v", err)
        }
        if err := os.WriteFile(destPath, compressed, 0644); err != nil {
            log.Fatalf("写入目标文件失败: %v", err)
        }
        log.Println("已将压缩文件替换到:", destPath)

        // 切换到 git 仓库目录
        gitDir := filepath.Dir(destDir)
        if err := os.Chdir(gitDir); err != nil {
            log.Fatalf("切换目录失败: %v", err)
        }
        relPath, _ := filepath.Rel(gitDir, destPath)
        if err := runGitCommands(relPath, release.TagName, push); err != nil {
            log.Fatalf("git 操作失败: %v", err)
        }
    } else {
        log.Println("目标文件已是最新，无需替换。")
    }
}
