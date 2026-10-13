package names

type Ref string

type Kind int

func local(name string) bool { return name != "" }

func Local(ref Ref) bool { return local(string(ref)) }
