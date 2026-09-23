# vendored CodeMirror 5.65.18

本目录为 MockNetPack 前端本地引入的 CodeMirror 5.65.18（MIT License），
用于「编辑规则」表单中回包体 JSON 的可折叠编辑器。

引入原因：/mocknetpack/ 静态资源经 SecurityHeadersMiddleware 下发
`Content-Security-Policy: default-src 'self'`，外部 CDN 脚本会被浏览器拦截，
故将核心 + JSON 折叠所需 addon 一并本地化，内网/离线环境亦可正常加载。

文件清单（均来自官方 5.65.18 发行版，未做任何修改）：

- lib/codemirror.min.js / lib/codemirror.min.css   核心编辑器
- mode/javascript/javascript.min.js                JS/JSON 语法模式
- addon/fold/foldcode.min.js                      折叠 API
- addon/fold/foldgutter.min.js + .css             行号旁折叠箭头
- addon/fold/brace-fold.min.js                    花括号/方括号折叠
- addon/edit/matchbrackets.min.js                 括号配对高亮
- addon/edit/closebrackets.min.js                 输入时自动补全右括号

License: MIT（CodeMirror 官方许可证，见 https://codemirror.net/LICENSE）
