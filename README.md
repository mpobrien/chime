#### building

```
go build -o chime ./*.go
```

#### using

*Add a job*
`chime add 'command-to-run'`

*List jobs*
`chime list`

*Pop the next pending job from the queue and run it* 
`chime run`

*Remove a job from the queue without running it*
`chime remove <job id>`

