package log

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"time"
)

var webHookUrl = "https://qyapi.weixin.qq.com/cgi-bin/webhook/send"

type Content struct {
	Content       string   `json:"content"`
	MentionedList []string `json:"mentioned_list,omitempty"`
}
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
	bytes, _ := json.Marshal(msg)
	req, _ := http.NewRequest("POST", webHookUrl+"?key="+token+"&debug=1", strings.NewReader(string(bytes)))
	req.Header.Set("Content-Type", "application/json")
	resp, err := (&http.Client{Timeout: 5 * time.Second}).Do(req)
	if err != nil {
		return
	}
	defer func(Body io.ReadCloser) {
		err = Body.Close()
		if err != nil {
		}
	}(resp.Body)

	_, err = io.ReadAll(resp.Body)
	if err != nil {
		return
	}
}
