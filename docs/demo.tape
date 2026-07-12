# Regenerate docs/demo.gif and docs/screenshot.png:
#
#   vhs docs/demo.tape
#
# Requires VHS (https://github.com/charmbracelet/vhs) and a monospace font with
# Braille glyphs installed (e.g. DejaVu Sans Mono — the default JetBrains Mono
# does NOT render the braille graphs). Uses `tankertop --demo` so no cluster is
# needed.

Output docs/demo.gif

Set Shell "bash"
Set FontFamily "DejaVu Sans Mono"
Set FontSize 14
Set Width 1360
Set Height 820
Set Padding 16
Set Framerate 24

Hide
Type "go build -o tankertop . && HOME=$(mktemp -d) ./tankertop --demo -interval 1s"
Enter
Sleep 5s
Show
Screenshot docs/screenshot.png
Sleep 3s

Down
Sleep 700ms
Down
Sleep 700ms
Down
Sleep 2s
Type "o"
Sleep 3s
Type "t"
Sleep 3s
Down
Sleep 700ms
Down
Sleep 1s
Space
Sleep 3s
Type "t"
Sleep 2s
Tab
Sleep 3s
Tab
Type "2"
Sleep 4s
Type "3"
Sleep 4s
Type "4"
Sleep 4s
Type "5"
Sleep 4s
Type "1"
Sleep 2500ms
Type "T"
Sleep 2200ms
Type "T"
Sleep 2200ms
Type "T"
Sleep 2200ms
Type "1"
Sleep 2s
Type "q"
