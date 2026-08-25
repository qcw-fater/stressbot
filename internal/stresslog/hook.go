package stresslog

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"time"
)

var webHookURL = "https://qyapi.weixin.qq.com/cgi-bin/webhook/send"

var webhookHTTPClient = &http.Client{Timeout: 5 * time.Second}

// Content 是企业微信 markdown 消息的正文体，MentionedList 声明需要 @ 的成员。
type Content struct {
	Content       string   `json:"content"`
	MentionedList []string `json:"mentioned_list,omitempty"`
}

// WebHookMsg 是企业微信 webhook 的消息信封：Msgtype 固定为 markdown，
// 正文由 Markdown 携带。
type WebHookMsg struct {
	Msgtype  string  `json:"msgtype"`
	Markdown Content `json:"markdown"`
}

// token 企业微信Hook的密钥
var token string

// SetWechatToken 设置企业微信Hook的密钥
func SetWechatToken(key string) {
	token = key
}

// postQYWXMsg 向企业微信发送Markdown格式的消息
//
//	data string - 要发送的消息内容
//	该函数用于将传入的消息内容以Markdown格式发送到企业微信。
//	首先，通过调用getQYWXHookKey()函数获取企业微信Hook的密钥。
//	如果密钥为空，则直接返回，不进行后续操作。
//	然后，创建一个WebHookMsg结构体对象，并设置其Msgtype为"markdown"，
//	同时设置Markdown字段为包含消息内容的Content结构体对象。
//	接下来，使用json.Marshal()函数将WebHookMsg对象序列化为JSON格式的字节数组。
//	然后，创建一个HTTP POST请求，请求URL为webHookUrl+"?key="+密钥+"&debug=1"，
//	请求体为JSON格式的字节数组，请求头中设置Content-Type为"application/json"。
//	最后，使用http.Client的Do()方法发送请求，并读取响应内容。
//	注意：响应内容在此函数中并未进行任何处理，只是简单地读取并丢弃。
func postQYWXMsg(data string) {
	if token == "" {
		return
	}
	msg := WebHookMsg{}
	msg.Msgtype = "markdown"
	msg.Markdown = Content{
		Content: data,
	}
	payload, err := json.Marshal(msg)
	if err != nil {
		return
	}
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, webHookURL+"?key="+token+"&debug=1", bytes.NewReader(payload))
	if err != nil {
		return
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := webhookHTTPClient.Do(req)
	if err != nil {
		return
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()
}
