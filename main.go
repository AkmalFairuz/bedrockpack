package main

import (
	"bufio"
	"fmt"
	"github.com/akmalfairuz/bedrockpack/internal/stealer"
	"github.com/akmalfairuz/bedrockpack/pack"
	"os"
	"strings"
)

func printHelp() {
	fmt.Println("Usage:")
	fmt.Println("   bedrockpack")
	fmt.Println("      Open the interactive menu")
	fmt.Println("   bedrockpack decrypt <path to resource pack> <key>")
	fmt.Println("      Decrypt the resource pack using the given key")
	fmt.Println("   bedrockpack encrypt <path to resource pack> <key (optional)>")
	fmt.Println("      Encrypt the resource pack using either the given key or a generated key")
	fmt.Println("      Automatically minify all the JSON files")
	fmt.Println("      Automatically regenerate the UUID of the resource pack in manifest.json")
	fmt.Println("   bedrockpack download <server address> [raknet|nethernet]")
	fmt.Println("      Download and decrypt resource packs from a server you are allowed to access")
	fmt.Println("   bedrockpack bot <server address> [raknet|nethernet] [player]")
	fmt.Println("      Join as a bot, print coloured chat, and optionally follow a visible player")
	fmt.Println("   bedrockpack proxy <server address> [raknet|nethernet]")
	fmt.Println("      Open a Bedrock proxy on UDP 19141 and NetherNet TCP 26060")
}

func main() {
	args := os.Args[1:]
	if len(args) == 0 {
		if err := runMenu(); err != nil {
			fmt.Fprintln(os.Stderr, "Error:", err)
			os.Exit(1)
		}
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
		if err := stealer.DownloadAndDecrypt(args[1], "raknet"); err != nil {
			fmt.Fprintln(os.Stderr, "Error:", err)
			os.Exit(1)
		}
	case "download":
		if len(args) < 2 {
			printHelp()
			return
		}
		network := "raknet"
		if len(args) > 2 {
			network = args[2]
		}
		if err := stealer.DownloadAndDecrypt(args[1], network); err != nil {
			fmt.Fprintln(os.Stderr, "Error:", err)
			os.Exit(1)
		}
	case "bot":
		if len(args) < 2 {
			printHelp()
			return
		}
		network, target := "raknet", ""
		if len(args) > 2 {
			network = args[2]
		}
		if len(args) > 3 {
			target = strings.Join(args[3:], " ")
		}
		if err := stealer.RunBot(args[1], network, target); err != nil {
			fmt.Fprintln(os.Stderr, "Error:", err)
			os.Exit(1)
		}
	case "proxy":
		if len(args) < 2 {
			printHelp()
			return
		}
		network := "raknet"
		if len(args) > 2 {
			network = args[2]
		}
		if err := stealer.RunProxy(args[1], network); err != nil {
			fmt.Fprintln(os.Stderr, "Error:", err)
			os.Exit(1)
		}
	default:
		printHelp()
	}
}

func runMenu() error {
	reader := bufio.NewReader(os.Stdin)
	for {
		fmt.Println("\nBedrockPack")
		fmt.Println("1. Скачать и расшифровать ресурс-паки")
		fmt.Println("2. Войти как бот (чат и сопровождение игрока)")
		fmt.Println("3. Прокси (RakNet UDP 19141 + NetherNet TCP 26060)")
		fmt.Println("0. Выход")
		choice, err := readLine(reader, "> ")
		if err != nil {
			return err
		}
		if choice == "0" {
			return nil
		}
		if choice != "1" && choice != "2" && choice != "3" {
			fmt.Println("Выберите 1, 2, 3 или 0.")
			continue
		}

		address, err := readLine(reader, "Адрес сервера: ")
		if err != nil {
			return err
		}
		if address == "" {
			fmt.Println("Адрес не может быть пустым.")
			continue
		}

		fmt.Println("Транспорт: 1. RakNet  2. NetherNet")
		transport, err := readLine(reader, "Выбор (по умолчанию 1): ")
		if err != nil {
			return err
		}
		network := "raknet"
		if transport == "2" {
			network = "nethernet"
		} else if transport != "" && transport != "1" {
			fmt.Println("Выберите 1 или 2.")
			continue
		}

		if choice == "1" {
			return stealer.DownloadAndDecrypt(address, network)
		}
		if choice == "3" {
			return stealer.RunProxy(address, network)
		}
		target, err := readLine(reader, "Имя игрока для сопровождения (Enter — без сопровождения): ")
		if err != nil {
			return err
		}
		return stealer.RunBot(address, network, target)
	}
}

func readLine(reader *bufio.Reader, prompt string) (string, error) {
	fmt.Print(prompt)
	line, err := reader.ReadString('\n')
	if err != nil && len(line) == 0 {
		return "", err
	}
	return strings.TrimSpace(line), nil
}
