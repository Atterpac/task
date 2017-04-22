package task

import (
	"bytes"
	"context"
	"fmt"
	"log"
	"os"
	"strings"

	"github.com/go-task/task/execext"

	"github.com/spf13/pflag"
	"golang.org/x/sync/errgroup"
)

var (
	// TaskFilePath is the default Taskfile
	TaskFilePath = "Taskfile"

	// Force (--force or -f flag) forces a task to run even when it's up-to-date
	Force bool
	// Watch (--watch or -w flag) enables watch of a task
	Watch bool

	// Tasks constains the tasks parsed from Taskfile
	Tasks = make(map[string]*Task)
)

// Task represents a task
type Task struct {
	Cmds      []string
	Deps      []string
	Desc      string
	Sources   []string
	Generates []string
	Dir       string
	Vars      map[string]string
	Set       string
	Env       map[string]string
}

// Run runs Task
func Run() {
	log.SetFlags(0)

	args := pflag.Args()
	if len(args) == 0 {
		log.Println("task: No argument given, trying default task")
		args = []string{"default"}
	}

	var err error
	Tasks, err = readTaskfile()
	if err != nil {
		log.Fatal(err)
	}

	if HasCyclicDep(Tasks) {
		log.Fatal("Cyclic dependency detected")
	}

	// check if given tasks exist
	for _, a := range args {
		if _, ok := Tasks[a]; !ok {
			var err error = &taskNotFoundError{taskName: a}
			fmt.Println(err)
			printExistingTasksHelp()
			return
		}
	}

	if Watch {
		if err := WatchTasks(args); err != nil {
			log.Fatal(err)
		}
		return
	}

	for _, a := range args {
		if err = RunTask(context.Background(), a); err != nil {
			log.Fatal(err)
		}
	}
}

// RunTask runs a task by its name
func RunTask(ctx context.Context, name string) error {
	t, ok := Tasks[name]
	if !ok {
		return &taskNotFoundError{name}
	}

	if err := t.runDeps(ctx); err != nil {
		return err
	}

	if !Force && t.isUpToDate() {
		log.Printf(`task: Task "%s" is up to date`, name)
		return nil
	}

	for i := range t.Cmds {
		if err := t.runCommand(ctx, i); err != nil {
			return &taskRunError{name, err}
		}
	}
	return nil
}

func (t *Task) runDeps(ctx context.Context) error {
	vars, err := t.handleVariables()
	if err != nil {
		return err
	}

	g, ctx := errgroup.WithContext(ctx)

	for _, d := range t.Deps {
		dep := d

		g.Go(func() error {
			dep, err := ReplaceVariables(dep, vars)
			if err != nil {
				return err
			}

			if err = RunTask(ctx, dep); err != nil {
				return err
			}
			return nil
		})
	}

	if err = g.Wait(); err != nil {
		return err
	}
	return nil
}

func (t *Task) isUpToDate() bool {
	if len(t.Sources) == 0 || len(t.Generates) == 0 {
		return false
	}

	sourcesMaxTime, err := getPatternsMaxTime(t.Sources)
	if err != nil || sourcesMaxTime.IsZero() {
		return false
	}

	generatesMinTime, err := getPatternsMinTime(t.Generates)
	if err != nil || generatesMinTime.IsZero() {
		return false
	}

	return generatesMinTime.After(sourcesMaxTime)
}

func (t *Task) runCommand(ctx context.Context, i int) error {
	vars, err := t.handleVariables()
	if err != nil {
		return err
	}
	c, err := ReplaceVariables(t.Cmds[i], vars)
	if err != nil {
		return err
	}

	if strings.HasPrefix(c, "^") {
		c = strings.TrimPrefix(c, "^")
		if err = RunTask(ctx, c); err != nil {
			return err
		}
		return nil
	}

	dir, err := ReplaceVariables(t.Dir, vars)
	if err != nil {
		return err
	}

	envs, err := t.getEnviron(vars)
	if err != nil {
		return err
	}
	opts := &execext.RunCommandOptions{
		Context: ctx,
		Command: c,
		Dir:     dir,
		Env:     envs,
		Stdin:   os.Stdin,
		Stderr:  os.Stderr,
	}

	if t.Set == "" {
		log.Println(c)
		opts.Stdout = os.Stdout
		if err = execext.RunCommand(opts); err != nil {
			return err
		}
	} else {
		buff := bytes.NewBuffer(nil)
		opts.Stdout = buff
		if err = execext.RunCommand(opts); err != nil {
			return err
		}
		os.Setenv(t.Set, strings.TrimSpace(buff.String()))
	}
	return nil
}

func (t *Task) getEnviron(vars map[string]string) ([]string, error) {
	if t.Env == nil {
		return nil, nil
	}

	envs := os.Environ()

	for k, v := range t.Env {
		replacedValue, err := ReplaceVariables(v, vars)
		if err != nil {
			return nil, err
		}
		replacedKey, err := ReplaceVariables(k, vars)
		if err != nil {
			return nil, err
		}
		envs = append(envs, fmt.Sprintf("%s=%s", replacedKey, replacedValue))
	}
	return envs, nil
}
