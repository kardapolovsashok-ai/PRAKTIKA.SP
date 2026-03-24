package main

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"log"
	"net"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"
)

// PingResult содержит результат проверки одного хоста
type PingResult struct {
	Host    string
	Success bool
	Latency time.Duration
	Time    time.Time // время проверки
}

// pingHost пытается установить TCP-соединение с хостом на порт 80
// и возвращает результат замера.
func pingHost(host string) PingResult {
	start := time.Now()
	// Пытаемся соединиться, таймаут 2 секунды
	conn, err := net.DialTimeout("tcp", host+":80", 2*time.Second)
	if err != nil {
		return PingResult{
			Host:    host,
			Success: false,
			Latency: 0,
			Time:    start,
		}
	}
	conn.Close()
	latency := time.Since(start)
	return PingResult{
		Host:    host,
		Success: true,
		Latency: latency,
		Time:    start,
	}
}

// readHosts читает файл и возвращает список хостов (по одному на строку).
// Пустые строки и комментарии (начинающиеся с '#') игнорируются.
func readHosts(filename string) ([]string, error) {
	file, err := os.Open(filename)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	var hosts []string
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := scanner.Text()
		// Пропускаем пустые строки и комментарии
		if line == "" || line[0] == '#' {
			continue
		}
		hosts = append(hosts, line)
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return hosts, nil
}

// startListener читает результаты из канала и выводит их в stdout и в файл (если задан).
// Завершается, когда канал results будет закрыт.
func startListener(results <-chan PingResult, output *os.File) {
	for res := range results {
		status := "FAIL"
		latencyStr := ""
		if res.Success {
			status = "OK"
			latencyStr = res.Latency.String()
		}
		// Формат: время | хост | OK/FAIL | задержка
		line := fmt.Sprintf("%s | %s | %s | %s\n",
			res.Time.Format("15:04:05"),
			res.Host,
			status,
			latencyStr,
		)
		// Всегда пишем в консоль
		fmt.Print(line)
		// Если задан файл, пишем туда
		if output != nil {
			if _, err := output.WriteString(line); err != nil {
				log.Printf("Ошибка записи в файл: %v", err)
			}
		}
	}
}

// runPingRound запускает одну серию проверок для всех хостов.
// results — канал, в который воркеры отправляют результаты.
// output — файл для записи (может быть nil).
// Возвращает, когда все результаты обработаны (listener завершился).
func runPingRound(hosts []string, output *os.File) {
	// Буферизованный канал, чтобы воркеры не блокировались при отправке
	results := make(chan PingResult, len(hosts))

	// Запускаем listener (будет читать и выводить)
	var listenerWg sync.WaitGroup
	listenerWg.Add(1)
	go func() {
		defer listenerWg.Done()
		startListener(results, output)
	}()

	// Запускаем воркеры для каждого хоста
	var workersWg sync.WaitGroup
	for _, host := range hosts {
		workersWg.Add(1)
		go func(h string) {
			defer workersWg.Done()
			res := pingHost(h)
			results <- res
		}(host)
	}

	// Закрываем канал, когда все воркеры завершат отправку
	go func() {
		workersWg.Wait()
		close(results)
	}()

	// Ждём завершения listener (он выйдет после закрытия канала)
	listenerWg.Wait()
}

func main() {
	// Определяем флаги командной строки
	fileFlag := flag.String("f", "hosts.txt", "путь к файлу со списком хостов (по одному на строке)")
	outputFlag := flag.String("o", "", "путь к файлу для записи результатов (по умолчанию только stdout)")
	monitorFlag := flag.Bool("monitor", false, "режим мониторинга: повторять проверки с заданным интервалом")
	intervalFlag := flag.Duration("interval", 5*time.Second, "интервал между проверками в режиме мониторинга")
	flag.Parse()

	// Читаем список хостов
	hosts, err := readHosts(*fileFlag)
	if err != nil {
		log.Fatalf("Ошибка чтения файла хостов: %v", err)
	}
	if len(hosts) == 0 {
		log.Fatal("Список хостов пуст.")
	}

	// Открываем выходной файл, если указан
	var outputFile *os.File
	if *outputFlag != "" {
		outputFile, err = os.OpenFile(*outputFlag, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
		if err != nil {
			log.Fatalf("Ошибка открытия файла для вывода: %v", err)
		}
		defer outputFile.Close()
	}

	// Настройка graceful shutdown
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigChan
		fmt.Println("\nПолучен сигнал завершения. Останавливаемся...")
		cancel()
	}()

	// Если режим мониторинга — бесконечный цикл с интервалом
	if *monitorFlag {
		fmt.Printf("Режим мониторинга, интервал %v. Нажмите Ctrl+C для выхода.\n", *intervalFlag)
		for {
			select {
			case <-ctx.Done():
				fmt.Println("Программа завершена.")
				return
			default:
				runPingRound(hosts, outputFile)
			}

			// Ждём интервал или сигнал завершения
			select {
			case <-time.After(*intervalFlag):
				// продолжаем
			case <-ctx.Done():
				fmt.Println("Программа завершена.")
				return
			}
		}
	} else {
		// Однократная проверка
		runPingRound(hosts, outputFile)
	}
}
