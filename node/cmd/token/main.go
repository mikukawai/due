package main

import (
	"fmt"
	"time"

	"due/node/internal/auth"
)

func main() {
	auth.Init("due-game-server-secret-change-me", 2*time.Hour, 7*24*time.Hour)
	access, refresh, err := auth.GenerateToken(10001)
	if err != nil {
		panic(err)
	}
	fmt.Println("access_token: ", access)
	fmt.Println("refresh_token:", refresh)
}
