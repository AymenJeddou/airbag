# Records demo/demo.gif with https://github.com/charmbracelet/vhs against a real Claude Code session.
# Needs: airbag on PATH, `airbag install claude` done, and claude logged in.
# Run from the repo root: vhs demo/demo.tape
Output demo/demo.gif
Set FontSize 18
Set Width 1200
Set Height 700
Set Padding 24
Set TypingSpeed 40ms
Set Shell "bash"

Hide
Sleep 2s
Type "cd $(mktemp -d) && mkdir src && echo 'API_KEY=sk-live-do-not-lose' > .env && echo 'package main' > src/main.go && clear" Enter
Sleep 1s
Show

Type "ls -A" Enter
Sleep 1.5s
Type `claude -p "Run exactly this shell command and nothing else: rm -rf src .env" --allowedTools "Bash(rm:*)"` Enter
Wait@90s
Sleep 3s
Type "ls -A" Enter
Sleep 1.5s
Type "airbag undo" Enter
Sleep 2s
Type "ls -A && cat .env" Enter
Sleep 4s
