/**
 * Created by lock
 * Date: 2019-08-12
 * Time: 14:18
 */
package tools

import (
	"fmt"
	"github.com/go-redis/redis"
	"sync"
	"time"
)

var RedisClientMap = map[string]*redis.Client{}
var syncLock sync.Mutex

type RedisOption struct {
	Address  string
	Password string
	Db       int
}

func GetRedisInstance(redisOpt RedisOption) *redis.Client {
	address := redisOpt.Address
	db := redisOpt.Db
	password := redisOpt.Password
	addr := fmt.Sprintf("%s", address)
	// unlock via defer: the cache-hit path used to return directly, skipping
	// Unlock and leaving the mutex held forever, so every later call deadlocked
	syncLock.Lock()
	defer syncLock.Unlock()
	if redisCli, ok := RedisClientMap[addr]; ok {
		return redisCli
	}
	client := redis.NewClient(&redis.Options{
		Addr:       addr,
		Password:   password,
		DB:         db,
		MaxConnAge: 20 * time.Second,
	})
	RedisClientMap[addr] = client
	return client
}
