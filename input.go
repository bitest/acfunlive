// 命令输入相关
package main

import (
	"bufio"
	"encoding/json"
	"log"
	"os"
	"sort"
	"time"
)

// 正在直播的主播
type streaming streamer

// json
type sJSON struct {
	UID   int    `json:"uid"`
	Name  string `json:"name"`
	Title string `json:"title"`
	URL   string `json:"url"`
}

// 实现json.Marshaler接口
func (s streaming) MarshalJSON() ([]byte, error) {
	sj := sJSON{UID: s.UID, Name: s.Name, Title: streamer(s).getTitle(), URL: streamer(s).getURL()}
	return json.Marshal(sj)
}

// 列出正在直播的主播
func listLive() (streamings []streaming) {
	if *isNoGUI {
		log.Println("正在直播的主播：")
	}
	streamers.Lock()
	defer streamers.Unlock()
	for _, s := range streamers.crt {
		if s.isLiveOn() {
			if *isNoGUI {
				log.Println(s.longID() + "：" + s.getTitle() + " " + s.getURL())
			}
			streamings = append(streamings, streaming(s))
		}
	}

	sort.Slice(streamings, func(i, j int) bool {
		return streamings[i].UID < streamings[j].UID
	})
	return streamings
}

// 列出正在下载的直播视频
func listRecord() (recordings []streaming) {
	lInfoMap.Lock()
	for _, info := range lInfoMap.info {
		if info.isRecording {
			recordings = append(recordings, streaming{
				UID:  info.uid,
				Name: getName(info.uid),
			})
		}
	}
	lInfoMap.Unlock()

	sort.Slice(recordings, func(i, j int) bool {
		return recordings[i].UID < recordings[j].UID
	})
	if *isNoGUI {
		log.Println("正在下载的直播视频：")
		for _, r := range recordings {
			s := streamer(r)
			log.Println(s.longID() + "：" + s.getTitle() + " " + s.getURL())
		}
	}

	return recordings
}

// 列出正在下载的直播弹幕
func listDanmu() (danmu []streaming) {
	lInfoMap.Lock()
	for _, info := range lInfoMap.info {
		if info.isDanmu {
			danmu = append(danmu, streaming{
				UID:  info.uid,
				Name: getName(info.uid),
			})
		}
	}
	lInfoMap.Unlock()

	sort.Slice(danmu, func(i, j int) bool {
		return danmu[i].UID < danmu[j].UID
	})
	if *isNoGUI {
		log.Println("正在下载的直播弹幕：")
		for _, d := range danmu {
			s := streamer(d)
			log.Println(s.longID() + "：" + s.getTitle() + " " + s.getURL())
		}
	}

	return danmu
}

// 通知main()退出程序
func quitRun() {
	lPrintln("正在准备退出，请等待...")
	q := controlMsg{c: quit}
	mainCh <- q
}

// 处理输入
func handleInput() {
	defer func() {
		if err := recover(); err != nil {
			lPrintErr("Recovering from panic in handleInput(), the error is:", err)
			lPrintErr("输入处理发生错误，尝试重启输入处理")
			time.Sleep(2 * time.Second)
			go handleInput()
		}
	}()

	scanner := bufio.NewScanner(os.Stdin)
	for scanner.Scan() {
		handleAllCmd(scanner.Text())
	}
	err := scanner.Err()
	checkErr(err)
}
