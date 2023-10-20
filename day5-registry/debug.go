package geerpc

import (
	"fmt"
	"html/template"
	"net/http"
)

const debugText = `<html>
	<body>
	<title>GeeRPC Services</title>
	{{range .}}
	<hr>
	Service {{.Name}}
	<hr>
		<table>
		<th align=center>Method</th><th align=center>Calls</th>
		{{range $name, $mtype := .Method}}
			<tr>
			<td align=left font=fixed>{{$name}}({{$mtype.ArgType}}, {{$mtype.ReplyType}}) error</td>
			<td align=center>{{$mtype.NumCalls}}</td>
			</tr>
		{{end}}
		</table>
	{{end}}
	</body>
	</html>`

var debug = template.Must(template.New("RPC debug").Parse(debugText))

type debugHTTP struct {
	*Server
}
type debugService struct {
	Name   string
	Method map[string]*methodType
}

// 将服务集合通过模板渲染（将数据填充到模板中，生成最终的输出结果的过程）成HTTP响应并返回给客户端
func (server debugHTTP) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	//定义一个存储debugService结构的切片services。
	var services []debugService
	//通过迭代了一个服务映射表，并将每个服务的名称和方法填充到services模板标签中。
	//服务映射表存储了服务名及其对应的方法
	server.serviceMap.Range(func(namei, svci interface{}) bool {
		svc := svci.(*service)
		services = append(services, debugService{
			Name:   namei.(string),
			Method: svc.method,
		})
		return true
	})
	//使用debug.Execute()执行模板debug，并将services传递给模板（包含一些HTML或文本格式的内容）进行渲染。
	err := debug.Execute(w, services)
	if err != nil {
		_, _ = fmt.Fprintln(w, "rpc: error executing template: ", err.Error())
	}
}
