package userhome

import "os"

// userHomeDir は差し替え可能にして、テストで解決失敗を再現できるようにする
var userHomeDir = os.UserHomeDir
