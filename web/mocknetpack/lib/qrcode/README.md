# vendored qrcodejs 1.0.0

本目录为 MockNetPack 前端本地引入的 qrcodejs 1.0.0（davidshimjs/qrcodejs，MIT License），
用于「扫码连接」模态框中渲染配对二维码。

引入原因：/mocknetpack/ 静态资源经 SecurityHeadersMiddleware 下发
`Content-Security-Policy: default-src 'self'`，外部 CDN 脚本会被浏览器拦截，
故将二维码渲染库本地化，内网/离线环境亦可正常加载。

文件清单（来自官方 1.0.0 发行版，未做任何修改）：

- qrcode.min.js   二维码渲染（QRCode → canvas）

License: MIT（https://github.com/davidshimjs/qrcodejs 仓库 License 文件）
