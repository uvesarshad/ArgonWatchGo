# Database Monitoring Configuration Examples

ArgonWatchGo now supports monitoring for **MongoDB**, **MySQL**, **PostgreSQL**, and **Redis** databases!

## Configuration

Add database configurations to your `config/config.json` file in the `databases` array:

```json
{
  "databases": [
    {
      "id": "main-mongodb",
      "name": "Main MongoDB",
      "type": "mongodb",
      "host": "localhost",
      "port": 27017,
      "database": "myapp",
      "username": "admin",
      "password": "password"
    },
    {
      "id": "mysql-db",
      "name": "MySQL Database",
      "type": "mysql",
      "host": "localhost",
      "port": 3306,
      "database": "myapp",
      "username": "root",
      "password": "password"
    },
    {
      "id": "postgres-db",
      "name": "PostgreSQL",
      "type": "postgresql",
      "host": "localhost",
      "port": 5432,
      "database": "myapp",
      "username": "postgres",
      "password": "password"
    },
    {
      "id": "redis-cache",
      "name": "Redis Cache",
      "type": "redis",
      "host": "localhost",
      "port": 6379,
      "password": "password",
      "database": 0
    }
  ]
}
```

## Monitored Metrics

### MongoDB
- **Version** & **Uptime**
- **Connections**: Current, Available, Total Created
- **Operations**: Insert, Query, Update, Delete counts
- **Memory**: Resident & Virtual memory usage
- **Database**: Collections count, Data size, Storage size, Indexes

### MySQL
- **Version** & **Uptime**
- **Connections**: Current, Max used, Total
- **Queries**: Total queries, Slow queries, Queries per second
- **Threads**: Connected, Running, Cached
- **Traffic**: Bytes received & sent

### PostgreSQL
- **Version** & **Connections**
- **Transactions**: Commits, Rollbacks, Commit ratio
- **Cache**: Blocks read/hit, Hit ratio
- **Tuples**: Returned, Fetched, Inserted, Updated, Deleted
- **Database Size**

### Redis
- **Version** & **Uptime**
- **Connections**: Current, Total, Rejected
- **Memory**: Used, Peak, RSS
- **Stats**: Total commands, Ops/sec, Keys, Evicted/Expired keys
- **Keyspace**: Hits, Misses, Hit rate

## Security Notes

⚠️ **Important**: Database credentials are stored in plain text in `config.json`. Consider:
- Using environment variables for passwords
- Restricting file permissions on `config.json`
- Creating read-only database users for monitoring
- Using connection strings with limited privileges

## API Endpoints

- `GET /api/databases` - Get all database statuses
- `POST /api/databases` - Add a database to monitor
- `DELETE /api/databases/:id` - Remove a database from monitoring

## Troubleshooting

### Connection Failures
- Verify database is running and accessible
- Check firewall rules allow connections
- Confirm credentials are correct
- Ensure database user has necessary permissions

### Missing Metrics
- Some metrics require specific database versions
- Certain metrics need elevated privileges
- Check database logs for permission errors

## Example: Read-Only MongoDB User

```javascript
db.createUser({
  user: "monitoring",
  pwd: "secure_password",
  roles: [
    { role: "clusterMonitor", db: "admin" },
    { role: "read", db: "admin" }
  ]
})
```

## Example: Read-Only MySQL User

```sql
CREATE USER 'monitoring'@'localhost' IDENTIFIED BY 'secure_password';
GRANT PROCESS, REPLICATION CLIENT ON *.* TO 'monitoring'@'localhost';
GRANT SELECT ON performance_schema.* TO 'monitoring'@'localhost';
FLUSH PRIVILEGES;
```
