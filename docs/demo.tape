# Regenerate docs/demo.gif and docs/screenshot.png:
#
#   vhs docs/demo.tape
#
# Requires VHS (https://github.com/charmbracelet/vhs) and a monospace font with
# Braille glyphs installed (e.g. DejaVu Sans Mono — the default JetBrains Mono
# does NOT render the braille graphs). Uses `kubeview --demo` so no cluster is
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
Type "go build -o kubeview . && HOME=$(mktemp -d) ./kubeview --demo -interval 300ms"
Enter
Sleep 5s
Show
Screenshot docs/screenshot.png
Sleep 2s

Down
Sleep 400ms
Down
Sleep 400ms
Down
Sleep 1s
Type "o"
Sleep 2s
Type "t"
Sleep 2s
Down
Down
Sleep 500ms
Space
Sleep 1800ms
Type "t"
Sleep 1s
Tab
Sleep 2s
Tab
Type "2"
Sleep 2500ms
Type "3"
Sleep 2500ms
Type "4"
Sleep 2500ms
Type "5"
Sleep 2500ms
Type "1"
Sleep 1200ms
Type "T"
Sleep 1600ms
Type "T"
Sleep 1600ms
Type "T"
Sleep 1600ms
Type "T"
Sleep 1600ms
Type "T"
Sleep 1500ms
Type "q"
