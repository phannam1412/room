package main

import (
	"encoding/json"
	"fmt"
	"html/template"
	"log"
	"math/rand"
	"net/http"
	"os"
	"sync"
	"time"

	"github.com/fatih/color"
	"github.com/gorilla/mux"
	"github.com/gorilla/websocket"
	ttlcache "github.com/jellydator/ttlcache/v3"
	"github.com/urfave/cli/v2"
)

var upgrader = websocket.Upgrader{} // use default options

func broadcast(cons map[int]*websocket.Conn, messageType int, message []byte) error {
	for _, v := range cons {
		err := v.WriteMessage(messageType, message)
		if err != nil {
			return err
		}
	}
	return nil
}

type Req struct {
	Action string
	Key    string
	Value  string
}

type Room struct {
	mu    sync.Mutex
	cache *ttlcache.Cache[string, string]
	cons  map[int]*websocket.Conn
}

type RoomManager struct {
	m map[string]*Room
}

func NewRoomManager() *RoomManager {
	return &RoomManager{m: map[string]*Room{}}
}

func NewRoom() *Room {
	//go cache.Start() // starts automatic expired item deletion
	return &Room{
		mu: sync.Mutex{},
		cache: ttlcache.New[string, string](
			ttlcache.WithTTL[string, string](time.Minute * 15),
		),
		cons: map[int]*websocket.Conn{},
	}
}

func (r *RoomManager) Join(roomId string, name string, conn *websocket.Conn) error {
	if _, ok := r.m[roomId]; !ok {
		r.m[roomId] = NewRoom()
	}
	return r.m[roomId].Join(conn, name)
}

func (r *Room) Join(conn *websocket.Conn, name string) error {
	conId := time.Now().Nanosecond()
	r.mu.Lock()
	r.cons[conId] = conn
	r.mu.Unlock()
	fmt.Printf("SERVER: %s joined the room !\nthere are %d people online\n", name, len(r.cons))
	for {
		mt, message, err := conn.ReadMessage()
		if err != nil {
			fmt.Printf("SERVER: ReadMessage() error, will close and quit connection for %s. Error: %s\n", name, err.Error())
			r.mu.Lock()
			conn.Close()
			delete(r.cons, conId)
			r.mu.Unlock()
			break
		}
		fmt.Printf("SERVER: received %s's message '%s'\n", name, message)
		req := Req{}
		err = json.Unmarshal(message, &req) // parse message
		if err != nil {
			fmt.Printf("SERVER: json.Unmarshal() error, name: %s, message: %s, error: %s\n", name, message, err.Error())
			continue
		}
		r.cache.Set(req.Key, req.Value, ttlcache.DefaultTTL) // set cache
		m := map[string]string{}
		items := r.cache.Items()
		for k, v := range items {
			m[k] = v.Value()
		}
		b, err := json.Marshal(m) // unmarshal to json, ready to response to client
		if err != nil {
			fmt.Printf("SERVER: json.Marshal() error, name: %s, message: %s, error: %s\n", name, message, err.Error())
			continue
		}
		fmt.Printf("SERVER: will broadcast message '%s' to %d people\n", string(b), len(r.cons))
		err = broadcast(r.cons, mt, b) // broadcast to clients
		if err != nil {
			fmt.Printf("SERVER: writeToMultiple() error, name: %s, message: %s, error: %s\n", name, message, err.Error())
			continue
		}
	}
	return nil
}

/*
set:     {"action": "set", "key": "%s", "value": "%d"}
get:     {"action": "get", "key": "%s"}
all:     {"action": "all"}
del:     {"action": "del", "key": "%s"}
*/
func startWebsocketServer(port int) error {
	router := mux.NewRouter()
	rm := NewRoomManager()
	router.HandleFunc("/room/{roomId}", func(writer http.ResponseWriter, request *http.Request) {
		m := mux.Vars(request)
		roomId := m["roomId"]
		c, err := upgrader.Upgrade(writer, request, nil)
		if err != nil {
			log.Print("upgrade:", err)
			return
		}
		err = rm.Join(roomId, "test", c)
		if err != nil {
			writer.WriteHeader(500)
			writer.Write([]byte(err.Error()))
		}
	})
	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		homeTemplate.Execute(w, "ws://"+r.Host+"/echo")
	})
	fmt.Printf("server is listening at :%d !\n", port)
	return http.ListenAndServe(fmt.Sprintf(":%d", port), router)
}

func startNewPeople(name string, serverPort int) {
	c, _, err := websocket.DefaultDialer.Dial(fmt.Sprintf("ws://localhost:%d/room/test", serverPort), http.Header{
		"cookie": {name},
	})
	if err != nil {
		panic(err)
	}

	// read message from websocket
	go func() {
		for {
			_, m, err := c.ReadMessage()
			if err != nil {
				fmt.Printf("%s: ReadMessage() error %s, type %T\n", name, err.Error(), err)
				break
			}
			fmt.Printf("%s: received %s\n", name, string(m))
		}
	}()

	// write message to websocket
	for {
		r := rand.Intn(10) + 1 // prevent 0 for time.Sleep() to work
		time.Sleep(time.Second * time.Duration(r))
		if r == 1 {
			color.Red("%s: leaving...\n", name)
			err := c.Close()
			if err != nil {
				panic(err)
			}
			break
		}
		key := "foo"
		if r%2 == 0 {
			key = "bar"
		}
		mes := fmt.Sprintf(`{"action": "set", "key": "%s", "value": "%d"}`, key, rand.Intn(100))
		color.Green("%s: send message '%s'\n", name, mes)
		err := c.WriteMessage(websocket.TextMessage, []byte(mes))
		if err != nil {
			panic(err)
		}
	}
}

/*
TODO:
- multi-tenancy: can include room_id
- API for creating room_id
- auto expired room
*/
func main() {
	app := &cli.App{
		Flags: []cli.Flag{
			&cli.IntFlag{
				Name:    "port",
				Aliases: []string{"p"},
				Value:   8888,
			},
		},
		Action: func(context *cli.Context) error {
			return startWebsocketServer(context.Int("port"))
		},
		Commands: []*cli.Command{
			{
				Name: "test",
				Action: func(context *cli.Context) error {
					rand.Seed(time.Now().Unix())
					peoples := []string{"Alice", "Bob", "Mary", "Peter", "Slava", "John", "Henry", "Susan", "Tim", "Jeva"}
					for _, v := range peoples {
						go startNewPeople(v, context.Int("port"))
						time.Sleep(time.Millisecond * 100)
					}
					time.Sleep(time.Hour)
					return nil
				},
			},
		},
	}
	err := app.Run(os.Args)
	if err != nil {
		log.Fatal(err)
	}
}

var homeTemplate = template.Must(template.New("").Parse(`
<!DOCTYPE html>
<html>
<head>
<meta charset="utf-8">
<script>
window.addEventListener("load", function(evt) {

    var output = document.getElementById("output");
    var input = document.getElementById("input");
    var ws;

    var print = function(message) {
        var d = document.createElement("div");
        d.textContent = message;
        output.appendChild(d);
        output.scroll(0, output.scrollHeight);
    };

    document.getElementById("open").onclick = function(evt) {
        if (ws) {
            return false;
        }
        ws = new WebSocket("{{.}}");
        ws.onopen = function(evt) {
            print("OPEN");
        }
        ws.onclose = function(evt) {
            print("CLOSE");
            ws = null;
        }
        ws.onmessage = function(evt) {
            print("RESPONSE: " + evt.data);
        }
        ws.onerror = function(evt) {
            print("ERROR: " + evt.data);
        }
        return false;
    };

    document.getElementById("send").onclick = function(evt) {
        if (!ws) {
            return false;
        }
        print("SEND: " + input.value);
        ws.send(input.value);
        return false;
    };

    document.getElementById("close").onclick = function(evt) {
        if (!ws) {
            return false;
        }
        ws.close();
        return false;
    };

});
</script>
</head>
<body>
<table>
<tr><td valign="top" width="50%">
<p>Click "Open" to create a connection to the server,
"Send" to send a message to the server and "Close" to close the connection.
You can change the message and send multiple times.
<p>
<form>
<button id="open">Open</button>
<button id="close">Close</button>
<p><input id="input" type="text" value="Hello world!">
<button id="send">Send</button>
</form>
</td><td valign="top" width="50%">
<div id="output" style="max-height: 70vh;overflow-y: scroll;"></div>
</td></tr></table>
</body>
</html>
`))
