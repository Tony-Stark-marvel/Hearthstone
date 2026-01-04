# Hearthstone
炉石传说-战略陪伴工具

`github.com/gordonklaus/portaudio` 这个包不是纯 Go 写的，它是 **CGO** 包，底层依赖 C 语言的 PortAudio 库。如果你的系统里没有安装 PortAudio 的 `.h` 头文件和 `.lib/.dll` 动态库，或者 Go 编译器找不到它们，就会报这个错（因为该包里的 Go 文件都有 `// +build` 约束，检测不到 C 库就会被排除）。

---

### Windows 下的终极解决方案 (MSYS2)

在 Windows 上配置 CGO 环境最标准、最不容易出错的方法是使用 **MSYS2**。

#### 步骤 1：安装 MSYS2
1.  下载安装包：[https://www.msys2.org/](https://www.msys2.org/)
2.  安装完成后，打开 **MSYS2 UCRT64** (注意：一定要选 UCRT64 或 MINGW64，不要选 MSYS)。

#### 步骤 2：安装 GCC 和 PortAudio
在 MSYS2 的终端里，输入以下命令安装编译链和库：

```bash
# 更新源
pacman -Syu

# 安装 GCC 编译工具链
pacman -S mingw-w64-ucrt-x86_64-toolchain

# 安装 PortAudio 开发库
pacman -S mingw-w64-ucrt-x86_64-portaudio

# 安装 pkg-config (帮助 Go 找到库)
pacman -S mingw-w64-ucrt-x86_64-pkg-config
```

#### 步骤 3：配置 Windows 环境变量 (关键)
为了让你的 CMD、PowerShell 或 VS Code 也能用这些工具，你需要把 MSYS2 的 `bin` 目录加到系统 `PATH` 里。

1.  找到 MSYS2 的安装目录，默认是 `C:\msys64`。
2.  找到 UCRT64 的 bin 目录：`C:\msys64\ucrt64\bin`。
3.  按 `Win + R`，输入 `sysdm.cpl`，回车 -> **高级** -> **环境变量**。
4.  在 **系统变量** 的 `Path` 中，点击 **新建**，粘贴路径 `C:\msys64\ucrt64\bin`。
5.  **重启你的 VS Code 或终端**。

#### 步骤 4：验证并启用 CGO
重启终端后，验证安装：

```bash
gcc --version
pkg-config --cflags portaudio-2.0
```
如果上面两个命令都有输出（不报错），说明环境配好了。

最后，确保 Go 开启了 CGO：
```bash
go env -w CGO_ENABLED=1
```

现在再运行你的代码 `go run main.go`，应该就能通过编译了。

---

### 如果你不想安装 MSYS2 (替代方案)

如果你觉得安装 MSYS2 太麻烦，还有一个“偷懒”的办法，那就是**不使用 PortAudio**，而是改用纯 Go 实现的音频库（虽然性能或兼容性可能稍差）。

可以使用 `github.com/gen2brain/malgo` (MiniAudio binding，它自带了 C 代码，不需要额外安装库，但依然需要 GCC)。

如果连 GCC 都不想装... **那就没办法在 Go 里做实时音频输入输出了**。音频硬件交互在 Windows 上本质上都需要调用 C 接口。

**最推荐还是走 MSYS2 路线，一劳永逸。**