package sharedstate

import "github.com/redis/go-redis/v9"

// 写入类操作统一用 Redis Lua 脚本封装，保证「主操作 + 可选 EXPIRE + 索引 SADD」原子一致。
// 约定：KEYS[1]=真实数据 key，KEYS[2]=任务索引集合 key；ttl 以秒为单位，<=0 表示不设置过期。

// setScript: SET + 可选 EXPIRE + SADD 索引。
var setScript = redis.NewScript(`
local ttl = tonumber(ARGV[2])
if ttl > 0 then
  redis.call('SET', KEYS[1], ARGV[1], 'EX', ttl)
else
  redis.call('SET', KEYS[1], ARGV[1])
end
redis.call('SADD', KEYS[2], KEYS[1])
return 1
`)

// incrScript: INCRBY + 可选 EXPIRE + SADD 索引，返回新值。
var incrScript = redis.NewScript(`
local v = redis.call('INCRBY', KEYS[1], ARGV[1])
local ttl = tonumber(ARGV[2])
if ttl > 0 then
  redis.call('EXPIRE', KEYS[1], ttl)
end
redis.call('SADD', KEYS[2], KEYS[1])
return v
`)

// claimScript: SET NX (+EX) + 成功才 SADD 索引。返回 1=抢占成功，0=已被占用。
var claimScript = redis.NewScript(`
local ok
local ttl = tonumber(ARGV[2])
if ttl > 0 then
  ok = redis.call('SET', KEYS[1], ARGV[1], 'NX', 'EX', ttl)
else
  ok = redis.call('SET', KEYS[1], ARGV[1], 'NX')
end
if ok then
  redis.call('SADD', KEYS[2], KEYS[1])
  return 1
end
return 0
`)

// releaseScript: owner 匹配才 DEL。返回 1=已释放，0=未释放（不存在或非本人）。
var releaseScript = redis.NewScript(`
if redis.call('GET', KEYS[1]) == ARGV[1] then
  return redis.call('DEL', KEYS[1])
end
return 0
`)

// renewScript: owner 匹配才 EXPIRE。返回 1=已续租，0=未续租。
var renewScript = redis.NewScript(`
if redis.call('GET', KEYS[1]) == ARGV[1] then
  return redis.call('EXPIRE', KEYS[1], ARGV[2])
end
return 0
`)

// queuePushScript: RPUSH + 可选 EXPIRE + SADD 索引。
var queuePushScript = redis.NewScript(`
redis.call('RPUSH', KEYS[1], ARGV[1])
local ttl = tonumber(ARGV[2])
if ttl > 0 then
  redis.call('EXPIRE', KEYS[1], ttl)
end
redis.call('SADD', KEYS[2], KEYS[1])
return 1
`)

// hashSetScript: HSET + 可选 EXPIRE + SADD 索引。
var hashSetScript = redis.NewScript(`
redis.call('HSET', KEYS[1], ARGV[1], ARGV[2])
local ttl = tonumber(ARGV[3])
if ttl > 0 then
  redis.call('EXPIRE', KEYS[1], ttl)
end
redis.call('SADD', KEYS[2], KEYS[1])
return 1
`)

// hashIncrScript: HINCRBY + 可选 EXPIRE + SADD 索引，返回新值。
var hashIncrScript = redis.NewScript(`
local v = redis.call('HINCRBY', KEYS[1], ARGV[1], ARGV[2])
local ttl = tonumber(ARGV[3])
if ttl > 0 then
  redis.call('EXPIRE', KEYS[1], ttl)
end
redis.call('SADD', KEYS[2], KEYS[1])
return v
`)
