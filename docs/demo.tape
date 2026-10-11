# Record with: vhs docs/demo.tape   (needs `make build`; output is gitignored)
Output docs/demo.gif
Set Shell "zsh"
Set FontSize 16
Set Width 1100
Set Height 600
Set TypingSpeed 60ms

Hide
Type "export PATH=$PWD/bin:$PATH && clear" Enter
Show

Type "api --version" Enter
Sleep 1s
Type "api doctor" Enter
Sleep 3s
Type "api config show" Enter
Sleep 3s
