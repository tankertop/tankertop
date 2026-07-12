# Regenerate docs/demo.gif and docs/screenshot.png:
#
#   go build -o tankertop .   # (on a machine with Go; the Mac used for recording has none)
#   vhs docs/demo.tape
#
# Requires VHS (https://github.com/charmbracelet/vhs) and a monospace font with
# Braille glyphs installed (e.g. DejaVu Sans Mono — the default JetBrains Mono
# does NOT render the braille graphs). Runs the pre-built ./tankertop binary in
# --demo mode, so no cluster and no Go toolchain are needed at record time.

Output docs/demo.gif

Set Shell "bash"
Set FontFamily "DejaVu Sans Mono"
Set FontSize 14
Set Width 1360
Set Height 820
Set Padding 16
Set Framerate 24

Hide
Type "HOME=$(mktemp -d) ./tankertop --demo -interval 1s"
Enter
Sleep 6s
Show
Screenshot docs/screenshot.png
Sleep 3s

# Environment pane: declared spec env vs the live runtime env, secrets masked
Type "e"
Sleep 3500ms
Type "m"
Sleep 2500ms
Type "e"
Sleep 1500ms

# Detail pane braille plots (cpu / mem)
Type "v"
Sleep 3500ms
Type "v"
Sleep 1200ms

# Move the selection, sort, and fold the namespace tree
Down
Sleep 700ms
Down
Sleep 1500ms
Type "o"
Sleep 3s
Type "t"
Sleep 3s
Type "N"
Sleep 3s
Type "N"
Sleep 2s
Type "t"
Sleep 2s

# Logs pane
Tab
Sleep 3s
Tab

# The fullscreen views
Type "2"
Sleep 3500ms
Type "3"
Sleep 3500ms
Type "4"
Sleep 3500ms
Type "5"
Sleep 3500ms
Type "1"
Sleep 2500ms

# Themes
Type "T"
Sleep 2200ms
Type "T"
Sleep 2200ms
Type "T"
Sleep 2200ms
Type "1"
Sleep 2s
Type "q"
