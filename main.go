package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"github.com/gorilla/websocket"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"google.golang.org/protobuf/proto"
	"hall/protocol"
	"io/ioutil"
	"log"
	"net/http"
)

var addr = flag.String("addr", "0.0.0.0:8080", "http service address")
var upgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool {
		return true
	},
} // use default options
// websocket的handler使用了goroutine,因此不要在里面访问全局变量
func home(w http.ResponseWriter, r *http.Request) {
	c, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Print("upgrade:", err)
		return
	}
	defer c.Close()

	for {
		_, message, err := c.ReadMessage()
		if err != nil {
			log.Println("READ ERROR:", err)
			break
		}
		handler(message, c)
	}
}

func main() {
	flag.Parse()
	log.SetFlags(0)
	http.HandleFunc("/", home)
	log.Fatal(http.ListenAndServe(*addr, nil))
}

// 切片默认传引用,数组传值
func handler(message []byte, c *websocket.Conn) {
	defer func() {
		// 处理错误
		if err := recover(); err != nil {
			log.Println("[handler] recovered from ", err, c.RemoteAddr())
		}
	}()
	log.Println("receive message from ", c.RemoteAddr())
	var data protocol.TPackage
	err := proto.Unmarshal(message, &data)
	if err != nil {
		panic(err)
	}

	// 逻辑处理
	switch data.MainCmd {
	case protocol.MainCmdID_SYS:
		sysExecutor(&data, c)
	case protocol.MainCmdID_ACCOUNTS:
		accountsExecutor(&data, c)
	}
}

// struct 是传值的
func sysExecutor(data *protocol.TPackage, c *websocket.Conn) {
	switch data.SubCmd {
	// 心跳请求
	case protocol.SubCmdID_SYS_HEART_ASK:
		response := protocol.TPackage{
			MainCmd: protocol.MainCmdID_SYS,
			SubCmd:  protocol.SubCmdID_SYS_HEART_ACK,
		}
		buf, err := proto.Marshal(&response)
		if err != nil {
			log.Fatalln("Mashal data error:", err)
		}
		//[ERROR-MSG]:Could not decode a text frame as UTF-8
		//不能使用websocket.TextMessage,必须用websocket.BinaryMessage
		c.WriteMessage(websocket.BinaryMessage, buf)
	}
}

// TODO 响应消息封装为一个函数,代码重复利用
// 如果服务内部出错,比如访问数据库出错或者php出错就没必要返回给client了
// TODO err变量只需要一个
func accountsExecutor(data *protocol.TPackage, c *websocket.Conn) {
	defer func() {
		if err := recover(); err != nil {
			log.Println("[ERROR accountsExecutor]: ", err)
		}
	}()

	switch data.SubCmd {
	case protocol.SubCmdID_ACCOUNTS_TOKEN_LOGON_REQ:
		var request protocol.CTokenLogonReq
		err := proto.Unmarshal(data.Data, &request)
		if err != nil {
			panic(err)
		}

		type ResponseData struct {
			Uid      int32  `json:"uid"`
			Avatar   string `json:"avatar"`
			NickName string `json:"nickname"`
		}

		type Response struct {
			Code int32        `json:"code"`
			Data ResponseData `json:"data"`
			Msg  string       `json:"msg"`
		}
		url := "http://192.168.2.110:9002/server/login/checklogintoken"
		post := fmt.Sprintf("{\"token\":\"%s\"}", request.Token)
		body := doPost(url, post)
		if body == nil {
			panic("[ERROR doPost failed]")
		}
		//TODO 这里使用匿名对象会不会更好
		var resp Response
		err = json.Unmarshal(body, &resp)
		if err != nil {
			panic(err)
		}
		// 登录失败
		if resp.Code != 0 {
			log.Println("登录失败,错误信息:", resp.Msg)
			r := protocol.TPackage{
				MainCmd: protocol.MainCmdID_ACCOUNTS,
				SubCmd:  protocol.SubCmdID_ACCOUNTS_LOGON_FAIL_RESP,
			}
			r2, err := proto.Marshal(&r)
			if err != nil {
				panic(err)
			}
			c.WriteMessage(websocket.BinaryMessage, r2)
			return
		}

		// 从mongodb加载用户数据
		result := struct {
			UserID   int32
			Token    string
			NickName string
			Gold     int32
			Avatar   string
		}{}
		Collection, mongoClient := ConnectToMongo()
		if mongoClient == nil {
			panic("[ERROR ConnectToMongo failed!]")
		}
		defer func() {
			if err := mongoClient.Disconnect(context.TODO()); err != nil {
				panic(err)
			}
			log.Println("成功关闭mongodb连接")
		}()

		//TODO 这里可以自定一个Context来设置read或者write Mongodb的超时
		err = Collection.FindOne(context.TODO(), bson.D{{"UserID", resp.Data.Uid}}).Decode(&result)
		if err != nil {
			if err == mongo.ErrNoDocuments {
				// 插入一条记录
				// TODO 这里可能会出错,出错返回失败给client
				_, err := Collection.InsertOne(context.TODO(), bson.D{{"UserID", resp.Data.Uid},
					{"_id", resp.Data.Uid},
					{"NickName", resp.Data.NickName},
					{"Gold", 0},
					{"Avatar", resp.Data.Avatar}})
				if err != nil {
					panic(err)
				}
			} else {
				// TODO 如果查找出错了,返回失败给client
				panic(err)
			}
		}
		data := protocol.CLogonSuccessResp{
			UserID:   result.UserID,
			Token:    result.Token,
			NickName: result.NickName,
			Gold:     result.Gold,
			Avatar:   result.Avatar,
		}

		//TODO proto.Marshal封装为Marshal函数,返回buf,这个函数应该是不可能出错的
		buf, err := proto.Marshal(&data)
		if err != nil {
			panic(err)
		}

		r1 := protocol.TPackage{
			MainCmd: protocol.MainCmdID_ACCOUNTS,
			SubCmd:  protocol.SubCmdID_ACCOUNTS_LOGON_SUCCESS_RESP,
			Data:    buf,
		}

		r2, err := proto.Marshal(&r1)
		if err != nil {
			panic(err)
		}
		//TODO 发送数据可能出错,出错后关闭文件描述符和释放对应的资源
		c.WriteMessage(websocket.BinaryMessage, r2)
	}
}

// TODO 这里如果timeout如何处理
func doPost(url string, param string) []byte {
	defer func() {
		if err := recover(); err != nil {
			log.Println("[ERROR doPost]: ", err)
		}
	}()
	req, err := http.NewRequest("POST", url, bytes.NewBuffer([]byte(param)))
	req.Header.Set("Content-Type", "application/json")
	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		panic(err)
	}
	defer resp.Body.Close()
	//fmt.Println("status", resp.Status)
	//fmt.Println("response:", resp.Header)
	body, _ := ioutil.ReadAll(resp.Body)
	return body
}
