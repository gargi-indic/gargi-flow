package experiment

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/gargi-indic/gargi-flow/internal/sim/model"
	"github.com/gargi-indic/gargi-flow/internal/sim/report"
	"github.com/gargi-indic/gargi-flow/pkg/engine"
	"github.com/gargi-indic/gargi-flow/pkg/sim"
)

// CompiledExperiment is a validated experiment/v1 document together with its
// compiled sim/v1 model and the worlds it evaluates. It is immutable after
// Compile returns and safe to share between goroutines.
type CompiledExperiment struct {
	// Def is the parsed experiment/v1 document.
	Def *model.Experiment
	// BaseDir is the directory the experiment's relative paths (model and
	// policy files) are resolved against.
	BaseDir string
	// FS is the file system relative paths are read from (with
	// slash-separated paths); nil means the operating system's.
	FS fs.FS
	// Model is the compiled sim/v1 model referenced by Def.Model.
	Model *model.CompiledModel
	// Worlds lists the evaluated worlds in report order: Def.Worlds in
	// document order, or every world of the model sorted by id when
	// Def.Worlds is empty.
	Worlds []sim.World
	// Policy is the compiled policies.policy document, the template of
	// grid and random search. It is nil for other searches and files.
	Policy model.Parameterized
	// Reports lists the parsed reports of Def.Reports, in document order.
	Reports []report.Spec
}

// WorldPrior returns the prior probability of every world of Worlds, in
// order: Def.WorldPrior (worlds it leaves out get 0), or uniform when it is
// nil.
func (e *CompiledExperiment) WorldPrior() []float64 {
	prior := make([]float64, len(e.Worlds))
	for w, world := range e.Worlds {
		if e.Def.WorldPrior == nil {
			prior[w] = 1 / float64(len(e.Worlds))
		} else {
			prior[w] = e.Def.WorldPrior[string(world.ID)]
		}
	}
	return prior
}

// CVaRAlpha returns Def.CVaRAlpha, or model.DefaultCVaRAlpha when it is
// unset.
func (e *CompiledExperiment) CVaRAlpha() float64 {
	if e.Def.CVaRAlpha == 0 {
		return model.DefaultCVaRAlpha
	}
	return e.Def.CVaRAlpha
}

// formatFor returns the document format implied by the extension of path.
func formatFor(path string) (engine.Format, bool) {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".yaml", ".yml":
		return engine.FormatYAML, true
	case ".json":
		return engine.FormatJSON, true
	default:
		return "", false
	}
}

// addPrefixed copies the issues of err into errs with every path prefixed by
// prefix and a colon, the convention used for issues of referenced documents.
// An error that is not a validation error becomes a single issue at prefix.
func addPrefixed(errs *engine.ValidationErrors, prefix string, err error, code engine.ErrorCode) {
	var ve *engine.ValidationErrors
	if errors.As(err, &ve) && ve != nil {
		for _, is := range ve.Issues() {
			errs.Add(prefix+":"+is.Path, is.Code, is.Message)
		}
		return
	}
	errs.Add(prefix, code, err.Error())
}

// docSource reads the documents an experiment references: from the
// operating system's file system when fsys is nil (paths use filepath
// semantics and may be absolute), otherwise from fsys (slash-separated
// paths, as fs.FS requires).
type docSource struct {
	fsys fs.FS
}

// resolve returns p resolved against baseDir.
func (s docSource) resolve(baseDir, p string) string {
	if s.fsys == nil {
		if !filepath.IsAbs(p) {
			p = filepath.Join(baseDir, p)
		}
		return p
	}
	return path.Join(baseDir, p)
}

// dir returns the directory of the resolved path p.
func (s docSource) dir(p string) string {
	if s.fsys == nil {
		return filepath.Dir(p)
	}
	return path.Dir(p)
}

// read returns the contents of the resolved path p.
func (s docSource) read(p string) ([]byte, error) {
	if s.fsys == nil {
		return os.ReadFile(p)
	}
	return fs.ReadFile(s.fsys, p)
}

// readDocument reads the file at file (resolved against baseDir when
// relative) from src and reports read and format problems at at.
func readDocument(src docSource, baseDir, file string, at engine.Path, errs *engine.ValidationErrors) (string, []byte, engine.Format, bool) {
	file = src.resolve(baseDir, file)
	format, ok := formatFor(file)
	if !ok {
		errs.Add(at.String(), engine.CodeInvalidFormat, fmt.Sprintf("unsupported file extension %q", filepath.Ext(file)))
		return file, nil, "", false
	}
	data, err := src.read(file)
	if err != nil {
		errs.Add(at.String(), engine.CodeUnresolvedReference, fmt.Sprintf("failed to read file: %v", err))
		return file, nil, "", false
	}
	return file, data, format, true
}

// Compile loads and compiles the sim/v1 model referenced by def (relative to
// baseDir) with model.Compile and resolves the experiment's worlds. Issues of
// the model document are reported with paths prefixed by "$.model:". When
// policies.policy is set (grid and random search), that policy/v1 document
// is read relative to baseDir, parsed and compiled against the model into
// CompiledExperiment.Policy; its issues are prefixed by
// "$.policies.policy:". The reports list is parsed and checked against the
// worlds and the model's metrics (regret and robust need two worlds; voi
// needs an observed node, flip_point a numeric world param, and voi,
// flip_point and policy_tree a discrete model), and world_prior and
// cvar_alpha are validated. It returns nil errors on success; on failure it returns every problem found,
// sorted by path, code and message.
func Compile(def *model.Experiment, baseDir string, reg *engine.Registry) (*CompiledExperiment, *engine.ValidationErrors) {
	return compileFrom(def, docSource{}, baseDir, reg)
}

// CompileFS is Compile with every referenced document (the model, its
// graph, policies.policy and, when the experiment runs, policies.files)
// read from fsys instead of the operating system. Relative paths are joined
// to baseDir with path.Join (slash-separated, as fs.FS requires) and the
// compiled experiment keeps fsys in CompiledExperiment.FS. Problems are
// reported exactly as by Compile. A nil fsys reads from the operating
// system, like Compile.
func CompileFS(def *model.Experiment, fsys fs.FS, baseDir string, reg *engine.Registry) (*CompiledExperiment, *engine.ValidationErrors) {
	return compileFrom(def, docSource{fsys: fsys}, baseDir, reg)
}

// compileFrom implements Compile and CompileFS.
func compileFrom(def *model.Experiment, src docSource, baseDir string, reg *engine.Registry) (*CompiledExperiment, *engine.ValidationErrors) {
	errs := &engine.ValidationErrors{}
	if def == nil {
		errs.Add(engine.PathRoot.String(), engine.CodeMissingRequired, "experiment definition is nil")
		return nil, sortIssues(errs)
	}

	modelAt := engine.PathRoot.Field("model")
	modelPath, data, format, ok := readDocument(src, baseDir, def.Model, modelAt, errs)
	if !ok {
		return nil, sortIssues(errs)
	}
	simDef, err := model.ParseSimModel(data, format)
	if err != nil {
		addPrefixed(errs, modelAt.String(), err, engine.CodeInvalidFormat)
		return nil, sortIssues(errs)
	}
	m, merrs := model.CompileFS(simDef, src.fsys, src.dir(modelPath), reg)
	if merrs.Len() > 0 {
		addPrefixed(errs, modelAt.String(), merrs, engine.CodeInvalidValue)
		return nil, sortIssues(errs)
	}

	exp := &CompiledExperiment{Def: def, BaseDir: baseDir, FS: src.fsys, Model: m}
	if len(def.Worlds) == 0 {
		for _, id := range sortedKeys(m.Def.Worlds) {
			exp.Worlds = append(exp.Worlds, sim.World{ID: sim.WorldID(id), Params: m.Def.Worlds[id]})
		}
	} else {
		seen := make(map[string]bool, len(def.Worlds))
		worldsAt := engine.PathRoot.Field("worlds")
		for i, id := range def.Worlds {
			at := worldsAt.Index(i).String()
			params, ok := m.Def.Worlds[id]
			switch {
			case !ok:
				errs.Add(at, engine.CodeUnresolvedReference, fmt.Sprintf("unknown world %q", id))
			case seen[id]:
				errs.Add(at, engine.CodeDuplicateID, fmt.Sprintf("duplicate world %q", id))
			default:
				exp.Worlds = append(exp.Worlds, sim.World{ID: sim.WorldID(id), Params: params})
			}
			seen[id] = true
		}
	}
	if len(exp.Worlds) == 0 && errs.Len() == 0 {
		errs.Add(engine.PathRoot.Field("worlds").String(), engine.CodeMissingRequired, "the experiment has no worlds")
	}
	policyFile := def.Policies.Policy
	if policyFile == "" && def.Policies.Candidates != nil {
		policyFile = def.Policies.Candidates.Policy
	}
	if policyFile != "" {
		exp.Policy = compilePolicyFile(src, baseDir, policyFile, m, errs)
	}
	validateReports(exp, errs)
	if errs.Len() > 0 {
		return nil, sortIssues(errs)
	}
	return exp, nil
}

// compilePolicyFile reads, parses and compiles the policies.policy document
// at file (relative to baseDir) against m. Problems are reported at
// $.policies.policy, or for issues inside the document at
// "$.policies.policy:" followed by the path within the policy document.
func compilePolicyFile(src docSource, baseDir, file string, m *model.CompiledModel, errs *engine.ValidationErrors) model.Parameterized {
	at := engine.PathRoot.Field("policies").Field("policy")
	_, data, format, ok := readDocument(src, baseDir, file, at, errs)
	if !ok {
		return nil
	}
	def, err := model.ParsePolicy(data, format)
	if err != nil {
		addPrefixed(errs, at.String(), err, engine.CodeInvalidFormat)
		return nil
	}
	p, perrs := model.CompilePolicy(def, m)
	if perrs.Len() > 0 {
		addPrefixed(errs, at.String(), perrs, engine.CodeInvalidValue)
		return nil
	}
	pp, ok := p.(model.Parameterized)
	if !ok {
		errs.Add(at.String(), engine.CodeInvalidValue, fmt.Sprintf("policy %q is not parameterized", p.ID()))
		return nil
	}
	return pp
}

// LoadFile reads and parses the experiment/v1 document at path with
// model.ParseExperiment and compiles it with Compile, resolving relative
// paths against the directory of path. Errors are *engine.ValidationErrors
// with sorted issues.
func LoadFile(path string, reg *engine.Registry) (*CompiledExperiment, error) {
	return load(docSource{}, path, reg)
}

// LoadFS is LoadFile with the experiment document at file, and every
// document it references, read from fsys (see CompileFS). Relative paths
// resolve against path.Dir(file). A nil fsys reads from the operating
// system, like LoadFile.
func LoadFS(fsys fs.FS, file string, reg *engine.Registry) (*CompiledExperiment, error) {
	return load(docSource{fsys: fsys}, file, reg)
}

// load implements LoadFile and LoadFS.
func load(src docSource, file string, reg *engine.Registry) (*CompiledExperiment, error) {
	errs := &engine.ValidationErrors{}
	_, data, format, ok := readDocument(src, "", file, engine.PathRoot, errs)
	if !ok {
		return nil, errs
	}
	def, err := model.ParseExperiment(data, format)
	if err != nil {
		return nil, err
	}
	exp, cerrs := compileFrom(def, src, src.dir(file), reg)
	if cerrs.Len() > 0 {
		return nil, cerrs
	}
	return exp, nil
}

// FilesSource returns a FilesSource for the experiment's policies.files,
// resolved against BaseDir and read from FS.
func (e *CompiledExperiment) FilesSource() FilesSource {
	return FilesSource{BaseDir: e.BaseDir, FS: e.FS, Files: e.Def.Policies.Files}
}
