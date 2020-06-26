// 直播下载相关
package main

import (
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sync"
	"time"
	"unicode/utf8"
)

// record用来传递下载信息
type record struct {
	stdin  io.WriteCloser     // ffmpeg的stdin
	cancel context.CancelFunc // 用来强行停止ffmpeg运行
	ch     chan control       // 下载goroutine的管道
}

// 存放某些没在recordMap的下载
var danglingRec struct {
	mu      sync.Mutex // records的锁
	records []record
}

// 查看并获取FFmpeg的位置
func getFFmpeg() (ffmpegFile string) {
	ffmpegFile = "ffmpeg"
	// linux和macOS下确认有没有安装FFmpeg
	if runtime.GOOS == "linux" || runtime.GOOS == "darwin" {
		_, err := exec.LookPath(ffmpegFile)
		if err != nil {
			lPrintln("系统没有安装FFmpeg")
			return ""
		}
	}
	// windows下ffmpeg.exe需要和本程序exe放在同一文件夹下
	if runtime.GOOS == "windows" {
		ffmpegFile = filepath.Join(exeDir, "ffmpeg.exe")
		_, err := os.Stat(ffmpegFile)
		if os.IsNotExist(err) {
			lPrintln("ffmpeg.exe需要和本程序放在同一文件夹下")
			return ""
		}
	}
	return ffmpegFile
}

// 转换文件名和限制文件名长度
func transFilename(filename string) (string, bool) {
	// 转换文件名不允许的特殊字符
	var re *regexp.Regexp
	if runtime.GOOS == "linux" {
		re = regexp.MustCompile(`[/]`)
	}
	if runtime.GOOS == "windows" {
		re = regexp.MustCompile(`[<>:"/\\|?*]`)
	}
	filename = re.ReplaceAllString(filename, "-")
	// linux和macOS下限制文件名长度
	if runtime.GOOS == "linux" || runtime.GOOS == "darwin" {
		if len(filename) > 250 {
			filename = filename[(len(filename) - 250):]
		}
	}
	outFilename := filepath.Join(exeDir, filename)
	// windows下全路径文件名不能过长
	if runtime.GOOS == "windows" {
		if utf8.RuneCountInString(outFilename) > 255 {
			lPrintln("全路径文件名太长，取消下载")
			return "", false
		}
	}
	return outFilename, true
}

// 设置自动下载指定主播的直播视频
func addRecord(uid int) bool {
	isExist := false
	streamers.mu.Lock()
	if s, ok := streamers.crt[uid]; ok {
		isExist = true
		if s.Record {
			lPrintln("已经设置过自动下载" + s.Name + "的直播视频")
		} else {
			s.Record = true
			sets(s)
			lPrintln("成功设置自动下载" + s.Name + "的直播视频")
		}
	}
	streamers.mu.Unlock()

	if !isExist {
		name := getName(uid)
		if name == "" {
			lPrintln("不存在uid为" + itoa(uid) + "的用户")
			return false
		}

		newStreamer := streamer{UID: uid, Name: name, Notify: false, Record: true}
		streamers.mu.Lock()
		sets(newStreamer)
		streamers.mu.Unlock()
		lPrintln("成功设置自动下载" + name + "的直播视频")
	}

	saveConfig()
	return true
}

// 取消自动下载指定主播的直播视频
func delRecord(uid int) bool {
	streamers.mu.Lock()
	if s, ok := streamers.crt[uid]; ok {
		if s.Notify || s.Danmu {
			s.Record = false
			sets(s)
		} else {
			deleteStreamer(uid)
		}
		lPrintln("成功取消自动下载" + s.Name + "的直播视频")
	} else {
		lPrintln("没有设置过自动下载uid为" + itoa(uid) + "的主播的直播视频")
	}
	streamers.mu.Unlock()

	saveConfig()
	return true
}

// 临时下载指定主播的直播视频
func startRec(uid int) bool {
	name := getName(uid)
	if name == "" {
		lPrintln("不存在uid为" + itoa(uid) + "的用户")
		return false
	}
	s := streamer{UID: uid, Name: name}

	msgMap.mu.Lock()
	if m, ok := msgMap.msg[s.UID]; ok && m.recording {
		lPrintln("已经在下载" + s.longID() + "的直播视频，如要重启下载，请先运行 stoprecord " + s.itoa())
		msgMap.mu.Unlock()
		return false
	}
	msgMap.mu.Unlock()

	if !s.isLiveOn() {
		lPrintln(s.longID() + "不在直播，取消下载直播视频")
		return false
	}

	ffmpegFile := getFFmpeg()
	if ffmpegFile == "" {
		desktopNotify("没有找到FFmpeg，停止下载直播视频")
		return false
	}

	// 查看程序是否处于监听状态
	if *isListen {
		// goroutine是为了快速返回
		go s.recordLive(ffmpegFile)
	} else {
		// 程序只在单独下载一个直播视频，不用goroutine，防止程序提前结束运行
		s.recordLive(ffmpegFile)
	}
	return true
}

// 停止下载指定主播的直播视频
func stopRec(uid int) bool {
	// web服务需要快速返回
	go func() {
		msgMap.mu.Lock()
		if m, ok := msgMap.msg[uid]; ok && m.recording {
			s := streamer{UID: uid, Name: getName(uid)}
			lPrintln("开始停止下载" + s.longID() + "的直播视频")
			m.rec.ch <- stopRecord
			io.WriteString(m.rec.stdin, "q")
			// 等待20秒强关下载，goroutine是为了防止锁住时间过长
			go func() {
				time.Sleep(20 * time.Second)
				m.rec.cancel()
			}()
			// 需要设置recording为false
			m.recording = false
			msgMap.msg[uid] = m
		} else {
			lPrintln("没有在下载uid为" + itoa(uid) + "的主播的直播视频")
		}
		msgMap.mu.Unlock()
	}()

	return true
}

// 下载主播的直播视频
func (s streamer) recordLive(ffmpegFile string) {
	defer func() {
		if err := recover(); err != nil {
			lPrintln("Recovering from panic in recordLive(), the error is:", err)
			lPrintln("下载" + s.longID() + "的直播视频发生错误，如要重启下载，请运行 startrecord " + s.itoa())
			desktopNotify("下载" + s.Name + "的直播视频发生错误")
			msgMap.mu.Lock()
			m := msgMap.msg[s.UID]
			m.recording = false
			msgMap.msg[s.UID] = m
			msgMap.mu.Unlock()
			deleteMsg(s.UID)
		}
	}()

	if ffmpegFile == "" {
		desktopNotify("没有找到FFmpeg，停止下载直播视频")
		return
	}

	// 下载hls直播源，想下载flv直播源的话可手动更改此处
	liveURL, _, streamName, cfg := s.getStreamURL()
	if liveURL == "" {
		lPrintln("无法获取" + s.longID() + "的直播源，退出下载直播视频，如要重启下载直播视频，请运行 startrecord " + s.itoa())
		desktopNotify("无法获取" + s.Name + "的直播源，退出下载直播视频")
		return
	}

	filename := getTime() + " " + s.Name + " " + s.getTitle()
	recordFile, ok := transFilename(filename)
	if !ok {
		return
	}
	// 想要输出其他视频格式可以修改这里的mp4
	recordFile = recordFile + ".mp4"

	lPrintln("开始下载" + s.longID() + "的直播视频")
	lPrintln("本次下载的视频文件保存在" + recordFile)
	if *isListen {
		lPrintln("如果想提前结束下载" + s.longID() + "的直播视频，运行 stoprecord " + s.itoa())
	}
	if s.Danmu {
		desktopNotify("开始下载" + s.Name + "的直播视频和弹幕")
	} else {
		desktopNotify("开始下载" + s.Name + "的直播视频")
	}

	// 运行ffmpeg下载直播视频
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cmd := exec.CommandContext(ctx, ffmpegFile,
		"-timeout", "10000000",
		"-i", liveURL,
		"-c", "copy", recordFile)

	stdin, err := cmd.StdinPipe()
	checkErr(err)
	defer stdin.Close()
	ch := make(chan control, 20)
	rec := record{stdin: stdin, cancel: cancel, ch: ch}
	msgMap.mu.Lock()
	if m, ok := msgMap.msg[s.UID]; ok {
		m.recording = true
		m.rec = rec
		m.streamName = streamName
		msgMap.msg[s.UID] = m
	} else {
		msgMap.msg[s.UID] = sMsg{recording: true, rec: rec, streamName: streamName}
	}
	msgMap.mu.Unlock()

	if !*isListen {
		// 程序单独下载一个直播视频时可以按q键退出（ffmpeg的特性）
		cmd.Stdin = os.Stdin
		lPrintln("按q键退出下载直播视频")
	}

	// 下载弹幕
	if s.Danmu {
		go s.getDanmu(ctx, cfg, filename)
	}

	err = cmd.Run()
	if err != nil {
		lPrintln("下载"+s.longID()+"的直播视频出现错误，尝试重启下载：", err)
	}

	if s.isLiveOn() {
		select {
		case msg := <-ch:
			switch msg {
			// 收到下播的信号
			case liveOff:
			// 收到停止下载的信号
			case stopRecord:
			default:
				lPrintln("未知的controlMsg：", msg)
			}
		default:
			// 程序处于监听状态时重启下载，否则不重启
			if *isListen {
				// 由于某种原因导致下载意外结束
				lPrintln("因意外结束下载" + s.longID() + "的直播视频，尝试重启下载")
				// 延迟两秒，防止意外情况下刷屏
				time.Sleep(2 * time.Second)
				go s.recordLive(ffmpegFile)
			}
		}
	} else {
		msgMap.mu.Lock()
		m := msgMap.msg[s.UID]
		m.recording = false
		msgMap.msg[s.UID] = m
		msgMap.mu.Unlock()
	}

	deleteMsg(s.UID)

	lPrintln(s.longID() + "的直播视频下载已经结束")
	if s.Danmu {
		desktopNotify(s.Name + "的直播视频和弹幕下载已经结束")
	} else {
		desktopNotify(s.Name + "的直播视频下载已经结束")
	}
}
