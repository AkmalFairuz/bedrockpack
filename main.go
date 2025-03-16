package main

import (
	"fmt"
	"github.com/akmalfairuz/bedrockpack/internal/stealer"
	"github.com/akmalfairuz/bedrockpack/pack"
	"os"
)

func printHelp() {
	fmt.Println("Usage:")
	fmt.Println("   bedrockpack decrypt <path to resource pack> <key>")
	fmt.Println("      Decrypt the resource pack using the given key")
	fmt.Println("   bedrockpack encrypt <path to resource pack> <key (optional)>")
	fmt.Println("      Encrypt the resource pack using either the given key or a generated key")
	fmt.Println("      Automatically minify all the JSON files")
	fmt.Println("      Automatically regenerate the UUID of the resource pack in manifest.json")
	fmt.Println("   bedrockpack steal <server ip:port>")
	fmt.Println("      Steal the resource pack from a server and decrypt it automatically")
	fmt.Println("      Xbox authentication is required")
}

func main() {
	args := os.Args[1:]
	if len(args) == 0 {
		printHelp()
		return
	}

	switch args[0] {
	case "encrypt":
		if len(args) < 2 {
			printHelp()
			return
		}

		fmt.Println("Loading " + args[1] + " resource pack...")
		rp, err := pack.LoadResourcePack(args[1])
		if err != nil {
			panic(err)
		}

		fmt.Println("Backup resource pack...")
		if err := rp.Save(args[1] + ".bak"); err != nil {
			panic(err)
		}

		var key []byte
		if len(args) > 2 {
			key = []byte(args[2])
		} else {
			key = pack.GenerateKey()
		}

		fmt.Println("Regenerate resource pack UUID...")
		if err := rp.RegenerateUUID(nil); err != nil {
			panic(err)
		}
		fmt.Printf("New resource pack UUID: %s\n", rp.UUID())

		fmt.Println("Minifying JSON files in resource pack...")
		if err := rp.MinifyJSONFiles(); err != nil {
			panic(err)
		}

		fmt.Println("Compressing .png files in resource pack...")
		if err := rp.CompressPNGFiles(); err != nil {
			panic(err)
		}

		fmt.Println("Encrypting resource pack with key " + string(key) + "...")
		if err := rp.Encrypt(key); err != nil {
			panic(err)
		}

		if err := rp.Save(args[1]); err != nil {
			panic(err)
		}
		_ = os.WriteFile(args[1]+".key.txt", key, 0777)
		fmt.Println("Resource pack encrypted!")
	case "decrypt":
		if len(args) < 3 {
			printHelp()
			return
		}

		fmt.Println("Loading " + args[1] + " resource pack...")
		rp, err := pack.LoadResourcePack(args[1])
		if err != nil {
			panic(err)
		}

		fmt.Println("Backup resource pack...")
		if err := rp.Save(args[1] + ".bak"); err != nil {
			panic(err)
		}

		key := []byte(args[2])
		fmt.Println("Decrypting resource pack with key " + string(key) + "...")
		if err := rp.Decrypt(key); err != nil {
			panic(err)
		}

		if err := rp.Save(args[1]); err != nil {
			panic(err)
		}
		fmt.Println("Resource pack decrypted!")
	case "steal":
		if len(args) < 2 {
			printHelp()
			return
		}
		stealer.Run(args[1])
	}
}
