# logging

The SyncGateway logging options are very complete, fine tunable, and clunky. This tries to consolidate it but keep the similar fine tune options.

## config

The original config:

```typescript
  type logging interface {
    log_file_path: string,
    redaction_level: string,
    console: {
      enabled: boolean,
      rotation: {
        max_size: number,
        max_age: number,
        localtime: boolean,
        rotated_logs_size_limit: number,
      },
      collation_buffer_size: number,
      log_level: string,
      log_keys: string[],
      color_enabled: boolean,
      file_output: string,
    },
    error: {
      enabled: boolean,
      rotation: {
        max_size: number,
        max_age: number,
        localtime: boolean,
        rotated_logs_size_limit: number,
      },
      collation_buffer_size: number,
    },
    warn: {
      enabled: boolean,
      rotation: {
        max_size: number,
        max_age: number,
        localtime: boolean,
        rotated_logs_size_limit: number,
      },
      collation_buffer_size: number,
    },
    info: {
      enabled: boolean,
      rotation: {
        max_size: number,
        max_age: number,
        localtime: boolean,
        rotated_logs_size_limit: number,
      },
      collation_buffer_size: number,
    },
    debug: {
      enabled: boolean,
      rotation: {
        max_size: number,
        max_age: number,
        localtime: boolean,
        rotated_logs_size_limit: number,
      },
      collation_buffer_size: number,
    },
    trace: {
      enabled: boolean,
      rotation: {
        max_size: number,
        max_age: number,
        localtime: boolean,
        rotated_logs_size_limit: number,
      },
      collation_buffer_size: number,
    },
    stats: {
      enabled: boolean,
      rotation: {
        max_size: number,
        max_age: number,
        localtime: boolean,
        rotated_logs_size_limit: number,
      },
      collation_buffer_size: number,
    }
  }
  ```

  new config:

  ```typescript
  type LogKey string; // TODO list out key options
  type LogLevel string;

  type logging interface {
    redaction_level: string, // TODO revisit
    color_enabled?: boolean, // TODO revisit
    log_level: LogLevel, // default level for all keys unless specifically overridden
    keys?: Record<LogKey, LogLevel>,
    localtime?: boolean, // TODO revisit
  }
  ```

## changes

1. lose the destinaltion options. Log only to stdout/stderr. All other logging should be handled outside of the service.
2. lose the separate logs for different levels. All output to the same place.
3. lose the `stats` log. use prometheus style stats instead.
4. allow separate logging levels for each `key`?
5. allow output format options? json, vs custom format, etc...

## possibilities

1. add sampling as per [zerolog sampling](https://github.com/rs/zerolog#log-sampling)