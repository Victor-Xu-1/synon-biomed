package executionprep

import (
	"reflect"
	"testing"
)

func TestShellCommandInDirectoryKeepsLiteralWorkingDirectory(t *testing.T) {
	directory, args, ok := ShellCommandInDirectory("cd 'engine files' && ./launcher --input '../data file.pdb'")
	if !ok || directory != "engine files" || len(args) != 3 || args[0] != "./launcher" || args[2] != "../data file.pdb" {
		t.Fatalf("directory=%q args=%#v ok=%t", directory, args, ok)
	}
	for _, source := range []string{
		"cd engine || ./launcher", "cd $(select_path) && ./launcher",
		"cd engine && ./launcher > result", "cd engine && ./launcher &",
		"cd engine && ./launcher; ./second", "cd - && ./launcher",
	} {
		if _, _, ok := ShellCommandInDirectory(source); ok {
			t.Fatalf("dynamic or compound operation was mistaken for one static invocation: %s", source)
		}
	}
}

func TestSingleShellCommandPreservesLiteralArgv(t *testing.T) {
	for _, source := range []string{
		"python 'my script.py' --name 'a$b'\n",
		"# heading\npython \"my script.py\" \\\n --name 'a$b' # footer\n",
		"python my\\ script.py --name a\\$b",
	} {
		args, ok := SingleShellCommand(source)
		if !ok || !reflect.DeepEqual(args, []string{"python", "my script.py", "--name", "a$b"}) {
			t.Fatalf("literal argv: %q -> %#v %t", source, args, ok)
		}
	}
}

func TestSingleShellCommandRejectsEffectsAndDynamicIdentity(t *testing.T) {
	for _, source := range []string{
		"python script.py\necho other", "python script.py; echo other", "python script.py | cat",
		"python script.py > file", "python script.py &", "! python script.py", "envvar=x python script.py",
		"python $(touch bad)", "python `touch bad`", "python $FILE", "python *.py", "python {a,b}.py",
		"python ~/script.py", "python <(echo code)", "(python script.py)", "", "# comment", "python\x00file",
	} {
		if args, ok := SingleShellCommand(source); ok {
			t.Fatalf("non-static command accepted: %q -> %#v", source, args)
		}
	}
}

func TestAppendShellArgumentsQuotesDataAndPreservesExistingArguments(t *testing.T) {
	command, ok := AppendShellArguments("python 'space path.py' # note\n", []string{"--name", "value;$(not-executable)"})
	if !ok {
		t.Fatal("static command refused")
	}
	args, ok := SingleShellCommand(command)
	if !ok || !reflect.DeepEqual(args, []string{"python", "space path.py", "--name", "value;$(not-executable)"}) {
		t.Fatalf("runtime argument changed identity: %q %#v", command, args)
	}
}
