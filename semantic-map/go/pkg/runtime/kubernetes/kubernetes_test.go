package kubernetes

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	apiruntime "k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"

	"github.com/DiyazY/di-agent/pkg/runtime"
	"github.com/DiyazY/di-agent/pkg/spec"
)

func f64(v float64) *float64 { return &v }

type podOpt func(*corev1.Pod)

func rl(cpu string) corev1.ResourceList {
	if cpu == "" {
		return nil
	}
	return corev1.ResourceList{corev1.ResourceCPU: resource.MustParse(cpu)}
}

// burstable is a running pod on node with a request and a limit.
func burstable(name, node, uid, request, limit string, opts ...podOpt) *corev1.Pod {
	p := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default", UID: types.UID(uid)},
		Spec: corev1.PodSpec{NodeName: node, Containers: []corev1.Container{{
			Name:      "c",
			Resources: corev1.ResourceRequirements{Requests: rl(request), Limits: rl(limit)},
		}}},
		Status: corev1.PodStatus{Phase: corev1.PodRunning, QOSClass: corev1.PodQOSBurstable},
	}
	for _, o := range opts {
		o(p)
	}
	return p
}

func newRT(node string, pods ...*corev1.Pod) (*Runtime, *fake.Clientset) {
	cs := fake.NewSimpleClientset()
	for _, p := range pods {
		if _, err := cs.CoreV1().Pods(p.Namespace).Create(context.Background(), p, metav1.CreateOptions{}); err != nil {
			panic(err)
		}
	}
	return New(runtime.Env{NodeID: node, Spec: spec.RuntimeSpec{Kind: "kubernetes"}}, cs), cs
}

func getPod(t *testing.T, cs *fake.Clientset, name string) *corev1.Pod {
	t.Helper()
	p, err := cs.CoreV1().Pods("default").Get(context.Background(), name, metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func workload(t *testing.T, r *Runtime, subject string) runtime.Workload {
	t.Helper()
	ws, err := r.Workloads(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, w := range ws {
		if w.Subject == subject {
			return w
		}
	}
	t.Fatalf("no workload %q in %+v", subject, ws)
	return runtime.Workload{}
}

func TestOnlyThisNodesPodsAreWorkloads(t *testing.T) {
	r, _ := newRT("n1", burstable("a", "n1", "ua", "100m", "1"), burstable("b", "n2", "ub", "100m", "1"))
	ws, err := r.Workloads(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(ws) != 1 || ws[0].Subject != "pod:ua" || ws[0].Name != "a" {
		t.Fatalf("workloads %+v; want only pod:ua named a", ws)
	}
}

func TestTheSpecsNodeOverridesTheNodeID(t *testing.T) {
	cs := fake.NewSimpleClientset(burstable("a", "k8s-name", "ua", "100m", "1"))
	r := New(runtime.Env{NodeID: "n1", Spec: spec.RuntimeSpec{Node: "k8s-name"}}, cs)
	if ws, _ := r.Workloads(context.Background()); len(ws) != 1 {
		t.Fatalf("workloads %+v", ws)
	}
}

func TestRuntimeAttributesAreLabels(t *testing.T) {
	p := burstable("a", "n1", "ua", "100m", "1", func(p *corev1.Pod) {
		p.Labels = map[string]string{"app": "web"}
		p.Spec.PriorityClassName = "system-node-critical"
		p.OwnerReferences = []metav1.OwnerReference{{Kind: "DaemonSet", Name: "x"}, {Kind: "Other"}}
	})
	r, _ := newRT("n1", p)
	l := workload(t, r, "pod:ua").Labels
	want := map[string]string{
		"app": "web", "k8s/namespace": "default", "k8s/priority-class": "system-node-critical",
		"k8s/qos": "Burstable", "k8s/owner-kind": "DaemonSet",
	}
	for k, v := range want {
		if l[k] != v {
			t.Errorf("label %s = %q, want %q (labels %v)", k, l[k], v, l)
		}
	}
}

func TestCPUSumsContainers(t *testing.T) {
	p := burstable("a", "n1", "ua", "100m", "500m")
	p.Spec.Containers = append(p.Spec.Containers, corev1.Container{Name: "d", Resources: corev1.ResourceRequirements{Requests: rl("200m"), Limits: rl("1")}})
	r, _ := newRT("n1", p)
	w := workload(t, r, "pod:ua")
	if w.CPU.Request == nil || *w.CPU.Request != 0.3 || w.CPU.Limit == nil || *w.CPU.Limit != 1.5 {
		t.Fatalf("cpu %+v", w.CPU)
	}
	p2 := burstable("b", "n1", "ub", "100m", "500m")
	p2.Spec.Containers = append(p2.Spec.Containers, corev1.Container{Name: "d"})
	r, _ = newRT("n1", p2)
	if w := workload(t, r, "pod:ub"); w.CPU.Limit != nil {
		t.Fatalf("limit %v; a container without one makes the pod's nil", *w.CPU.Limit)
	}
}

func TestApplyResizesAndAnnotates(t *testing.T) {
	r, cs := newRT("n1", burstable("a", "n1", "ua", "100m", "1"))
	exp := time.Unix(5000, 0).UTC()
	u, err := r.Apply(context.Background(), "pod:ua", runtime.Change{Action: runtime.ActionCPULimit, CPULimit: 0.3, Expires: exp, DecisionID: "d1"})
	if err != nil {
		t.Fatal(err)
	}
	if u.Runtime != "kubernetes" || u.NodeID != "n1" || u.Applied != 0.3 || u.Original.Limit == nil || *u.Original.Limit != 1 {
		t.Fatalf("undo %+v", u)
	}
	p := getPod(t, cs, "a")
	if got := p.Spec.Containers[0].Resources.Limits.Cpu().String(); got != "300m" {
		t.Fatalf("limit %s, want 300m", got)
	}
	var stored runtime.Undo
	if err := json.Unmarshal([]byte(p.Annotations[Annotation]), &stored); err != nil {
		t.Fatal(err)
	}
	if stored.Original.Limit == nil || *stored.Original.Limit != 1 || stored.Applied != 0.3 || stored.DecisionID != "d1" || !stored.Expires.Equal(exp) {
		t.Fatalf("annotation %+v", stored)
	}
}

func TestTheLimitIsRoundedDownToMillicores(t *testing.T) {
	r, cs := newRT("n1", burstable("a", "n1", "ua", "", "1"))
	if _, err := r.Apply(context.Background(), "pod:ua", runtime.Change{Action: runtime.ActionCPULimit, CPULimit: 0.2999}); err != nil {
		t.Fatal(err)
	}
	if got := getPod(t, cs, "a").Spec.Containers[0].Resources.Limits.Cpu().String(); got != "299m" {
		t.Fatalf("limit %s, want 299m", got)
	}
	if _, err := r.Apply(context.Background(), "pod:ua", runtime.Change{Action: runtime.ActionCPULimit, CPULimit: 0.0001}); err != nil {
		t.Fatal(err)
	}
	if got := getPod(t, cs, "a").Spec.Containers[0].Resources.Limits.Cpu().String(); got != "1m" {
		t.Fatalf("limit %s, want 1m", got)
	}
}

func TestRevertRestoresAndClears(t *testing.T) {
	r, cs := newRT("n1", burstable("a", "n1", "ua", "100m", "1"))
	ctx := context.Background()
	u, err := r.Apply(ctx, "pod:ua", runtime.Change{Action: runtime.ActionCPULimit, CPULimit: 0.3})
	if err != nil {
		t.Fatal(err)
	}
	if err := r.Revert(ctx, u); err != nil {
		t.Fatal(err)
	}
	p := getPod(t, cs, "a")
	if got := p.Spec.Containers[0].Resources.Limits.Cpu().String(); got != "1" {
		t.Fatalf("limit %s, want 1", got)
	}
	if _, ok := p.Annotations[Annotation]; ok {
		t.Fatalf("annotation still there: %v", p.Annotations)
	}
}

func TestASecondChangeKeepsTheFirstOriginal(t *testing.T) {
	r, cs := newRT("n1", burstable("a", "n1", "ua", "100m", "1"))
	ctx := context.Background()
	if _, err := r.Apply(ctx, "pod:ua", runtime.Change{Action: runtime.ActionCPULimit, CPULimit: 0.6}); err != nil {
		t.Fatal(err)
	}
	u, err := r.Apply(ctx, "pod:ua", runtime.Change{Action: runtime.ActionCPULimit, CPULimit: 0.3})
	if err != nil {
		t.Fatal(err)
	}
	if u.Original.Limit == nil || *u.Original.Limit != 1 || u.Applied != 0.3 {
		t.Fatalf("undo %+v; want original 1, applied 0.3", u)
	}
	if err := r.Revert(ctx, u); err != nil {
		t.Fatal(err)
	}
	if got := getPod(t, cs, "a").Spec.Containers[0].Resources.Limits.Cpu().String(); got != "1" {
		t.Fatalf("limit %s after revert, want 1", got)
	}
}

func annotated(v string) podOpt {
	return func(p *corev1.Pod) { p.Annotations = map[string]string{Annotation: v} }
}

func TestAnotherNodesChangeIsNeverOverwritten(t *testing.T) {
	foreign, _ := json.Marshal(runtime.Undo{NodeID: "n2", Subject: "pod:ua", Original: runtime.Resource{Limit: f64(9)}})
	r, cs := newRT("n1", burstable("a", "n1", "ua", "100m", "1", annotated(string(foreign))))
	if got := workload(t, r, "pod:ua").Can[runtime.ActionCPULimit]; got != "changed by another node: n2" {
		t.Fatalf("Can = %q", got)
	}
	if _, err := r.Apply(context.Background(), "pod:ua", runtime.Change{Action: runtime.ActionCPULimit, CPULimit: 0.5}); err == nil {
		t.Fatal("applied over another node's change")
	}
	p := getPod(t, cs, "a")
	if p.Annotations[Annotation] != string(foreign) || p.Spec.Containers[0].Resources.Limits.Cpu().String() != "1" {
		t.Fatalf("pod touched: %v", p.Annotations)
	}
	err := r.Revert(context.Background(), runtime.Undo{NodeID: "n1", Subject: "pod:ua", Original: runtime.Resource{Limit: f64(1)}})
	if err == nil || errors.Is(err, runtime.ErrGone) || getPod(t, cs, "a").Annotations[Annotation] != string(foreign) {
		t.Fatalf("revert err %v; want a plain error and the foreign record kept", err)
	}
}

func TestAGarbledAnnotationIsLeftAlone(t *testing.T) {
	r, cs := newRT("n1", burstable("a", "n1", "ua", "100m", "1", annotated("{not json")))
	if got := workload(t, r, "pod:ua").Can[runtime.ActionCPULimit]; got != "unreadable di-agent/change annotation" {
		t.Fatalf("Can = %q", got)
	}
	if _, err := r.Apply(context.Background(), "pod:ua", runtime.Change{Action: runtime.ActionCPULimit, CPULimit: 0.5}); err == nil {
		t.Fatal("applied over a garbled record")
	}
	if got := getPod(t, cs, "a").Annotations[Annotation]; got != "{not json" {
		t.Fatalf("annotation %q", got)
	}
	if ch, err := r.Changed(context.Background(), "n1"); err != nil || len(ch) != 0 {
		t.Fatalf("changed %+v err %v", ch, err)
	}
}

func failResize(cs *fake.Clientset, err error) {
	cs.PrependReactor("patch", "pods", func(a k8stesting.Action) (bool, apiruntime.Object, error) {
		if a.GetSubresource() == "resize" {
			return true, nil, err
		}
		return false, nil, nil
	})
}

func TestAFailedResizeLeavesTheRecord(t *testing.T) {
	r, cs := newRT("n1", burstable("a", "n1", "ua", "100m", "1"))
	failResize(cs, errors.New("timeout"))
	if _, err := r.Apply(context.Background(), "pod:ua", runtime.Change{Action: runtime.ActionCPULimit, CPULimit: 0.3}); err == nil {
		t.Fatal("want the resize error")
	}
	p := getPod(t, cs, "a")
	if p.Spec.Containers[0].Resources.Limits.Cpu().String() != "1" {
		t.Fatalf("limit changed: %v", p.Spec.Containers[0].Resources.Limits)
	}
	got, err := r.Changed(context.Background(), "n1")
	if err != nil || len(got) != 1 || *got[0].Original.Limit != 1 {
		t.Fatalf("changed %+v err %v; want the record", got, err)
	}
}

func TestANotFoundAboutSomethingElseIsNotGone(t *testing.T) {
	r, cs := newRT("n1", burstable("a", "n1", "ua", "100m", "1"))
	failResize(cs, apierrors.NewNotFound(schema.GroupResource{Resource: "pods/resize"}, "other"))
	_, err := r.Apply(context.Background(), "pod:ua", runtime.Change{Action: runtime.ActionCPULimit, CPULimit: 0.3})
	if err == nil || errors.Is(err, runtime.ErrGone) {
		t.Fatalf("err %v; a 404 not about this pod is not ErrGone", err)
	}
}

func TestResizeLeavesMemoryAndRequestsAlone(t *testing.T) {
	p := burstable("a", "n1", "ua", "100m", "1")
	p.Spec.Containers[0].Resources.Requests[corev1.ResourceMemory] = resource.MustParse("64Mi")
	p.Spec.Containers[0].Resources.Limits[corev1.ResourceMemory] = resource.MustParse("128Mi")
	r, cs := newRT("n1", p)
	if _, err := r.Apply(context.Background(), "pod:ua", runtime.Change{Action: runtime.ActionCPULimit, CPULimit: 0.3}); err != nil {
		t.Fatal(err)
	}
	res := getPod(t, cs, "a").Spec.Containers[0].Resources
	if res.Limits.Memory().String() != "128Mi" || res.Requests.Memory().String() != "64Mi" || res.Requests.Cpu().String() != "100m" {
		t.Fatalf("resources %+v", res)
	}
}

func TestAppliedIsTheValueSet(t *testing.T) {
	r, _ := newRT("n1", burstable("a", "n1", "ua", "", "1"))
	u, err := r.Apply(context.Background(), "pod:ua", runtime.Change{Action: runtime.ActionCPULimit, CPULimit: 0.2999})
	if err != nil || u.Applied != 0.299 {
		t.Fatalf("applied %v err %v; want 0.299", u.Applied, err)
	}
}

func TestRevertOnAFinishedPodIsErrGone(t *testing.T) {
	r, cs := newRT("n1", burstable("a", "n1", "ua", "100m", "1"))
	u, err := r.Apply(context.Background(), "pod:ua", runtime.Change{Action: runtime.ActionCPULimit, CPULimit: 0.3})
	if err != nil {
		t.Fatal(err)
	}
	p := getPod(t, cs, "a")
	p.Status.Phase = corev1.PodSucceeded
	if _, err := cs.CoreV1().Pods("default").Update(context.Background(), p, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	if err := r.Revert(context.Background(), u); !errors.Is(err, runtime.ErrGone) {
		t.Fatalf("err %v; want ErrGone", err)
	}
}

func TestChangedIgnoresOtherNodes(t *testing.T) {
	foreign, _ := json.Marshal(runtime.Undo{NodeID: "n2", Subject: "pod:ua"})
	p := burstable("a", "n1", "ua", "100m", "1", func(p *corev1.Pod) { p.Annotations = map[string]string{Annotation: string(foreign)} })
	r, _ := newRT("n1", p)
	got, err := r.Changed(context.Background(), "n1")
	if err != nil || len(got) != 0 {
		t.Fatalf("changed %+v err %v; want none", got, err)
	}
	got, err = r.Changed(context.Background(), "n2")
	if err != nil || len(got) != 1 {
		t.Fatalf("n2's changed %+v err %v; want its one", got, err)
	}
}

func TestChangedFindsWhatApplyRecorded(t *testing.T) {
	r, _ := newRT("n1", burstable("a", "n1", "ua", "100m", "1"), burstable("b", "n1", "ub", "100m", "1"))
	ctx := context.Background()
	if _, err := r.Apply(ctx, "pod:ub", runtime.Change{Action: runtime.ActionCPULimit, CPULimit: 0.5, Expires: time.Unix(10, 0)}); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Apply(ctx, "pod:ua", runtime.Change{Action: runtime.ActionCPULimit, CPULimit: 0.5, Expires: time.Unix(5, 0)}); err != nil {
		t.Fatal(err)
	}
	got, err := r.Changed(ctx, "n1")
	if err != nil || len(got) != 2 || got[0].Subject != "pod:ua" || got[1].Subject != "pod:ub" {
		t.Fatalf("changed %+v err %v; want ua then ub", got, err)
	}
}

func TestAGonePodIsErrGone(t *testing.T) {
	r, cs := newRT("n1", burstable("a", "n1", "ua", "100m", "1"))
	ctx := context.Background()
	u, err := r.Apply(ctx, "pod:ua", runtime.Change{Action: runtime.ActionCPULimit, CPULimit: 0.3})
	if err != nil {
		t.Fatal(err)
	}
	if err := cs.CoreV1().Pods("default").Delete(ctx, "a", metav1.DeleteOptions{}); err != nil {
		t.Fatal(err)
	}
	if err := r.Revert(ctx, u); !errors.Is(err, runtime.ErrGone) {
		t.Fatalf("revert err %v; want ErrGone", err)
	}
	if _, err := r.Apply(ctx, "pod:ua", runtime.Change{Action: runtime.ActionCPULimit, CPULimit: 0.2}); !errors.Is(err, runtime.ErrGone) {
		t.Fatalf("apply err %v; want ErrGone", err)
	}
}

func TestAPodThatRestartedUnderANewUIDIsErrGone(t *testing.T) {
	r, cs := newRT("n1", burstable("a", "n1", "ua", "100m", "1"))
	u, err := r.Apply(context.Background(), "pod:ua", runtime.Change{Action: runtime.ActionCPULimit, CPULimit: 0.3})
	if err != nil {
		t.Fatal(err)
	}
	p := getPod(t, cs, "a")
	p.UID = "ua2"
	if _, err := cs.CoreV1().Pods("default").Update(context.Background(), p, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	if err := r.Revert(context.Background(), u); !errors.Is(err, runtime.ErrGone) {
		t.Fatalf("revert err %v; want ErrGone", err)
	}
}

func TestAnUndoWithoutALimitIsAnErrorNotGone(t *testing.T) {
	r, _ := newRT("n1", burstable("a", "n1", "ua", "100m", "1"))
	err := r.Revert(context.Background(), runtime.Undo{Subject: "pod:ua", NodeID: "n1"})
	if err == nil || errors.Is(err, runtime.ErrGone) {
		t.Fatalf("err %v; want an error that is not ErrGone", err)
	}
}

func TestAPodThatCannotTakeALimitIsNotApplied(t *testing.T) {
	r, cs := newRT("n1", burstable("a", "n1", "ua", "100m", "1", func(p *corev1.Pod) { p.Status.Phase = corev1.PodPending }))
	if _, err := r.Apply(context.Background(), "pod:ua", runtime.Change{Action: runtime.ActionCPULimit, CPULimit: 0.3}); err == nil || !strings.Contains(err.Error(), "not running") {
		t.Fatalf("err %v; want the Can reason", err)
	}
	if got := getPod(t, cs, "a").Spec.Containers[0].Resources.Limits.Cpu().String(); got != "1" {
		t.Fatalf("limit %s; a refused apply must not touch the pod", got)
	}
}

func TestCanReasons(t *testing.T) {
	multi := burstable("m", "n1", "um", "100m", "1")
	multi.Spec.Containers = append(multi.Spec.Containers, corev1.Container{Name: "d", Resources: corev1.ResourceRequirements{Limits: rl("1")}})
	cases := []struct {
		name string
		pod  *corev1.Pod
		want string
	}{
		{"possible", burstable("p", "n1", "u", "100m", "1"), ""},
		{"BestEffort", burstable("p", "n1", "u", "", "", func(p *corev1.Pod) { p.Status.QOSClass = corev1.PodQOSBestEffort }),
			"BestEffort: a limit would change the pod's QoS class"},
		{"no limit", burstable("p", "n1", "u", "100m", ""),
			"no CPU limit: a limit the agent added could never be removed"},
		{"Guaranteed", burstable("p", "n1", "u", "1", "1", func(p *corev1.Pod) { p.Status.QOSClass = corev1.PodQOSGuaranteed }),
			"Guaranteed: changing the limit alone would change the pod's QoS class"},
		{"multi", multi, "more than one container: the limit cannot be split by the agent"},
		{"deleting", burstable("p", "n1", "u", "100m", "1", func(p *corev1.Pod) { n := metav1.Now(); p.DeletionTimestamp = &n }), "being deleted"},
		{"sidecar", burstable("p", "n1", "u", "100m", "1", func(p *corev1.Pod) {
			a := corev1.ContainerRestartPolicyAlways
			p.Spec.InitContainers = []corev1.Container{{Name: "s", RestartPolicy: &a}}
		}), "pod-level resources or sidecars: the agent cannot account for them"},
		{"pod-level resources", burstable("p", "n1", "u", "100m", "1", func(p *corev1.Pod) { p.Spec.Resources = &corev1.ResourceRequirements{Limits: rl("2")} }),
			"pod-level resources or sidecars: the agent cannot account for them"},
		{"pending", burstable("p", "n1", "u", "100m", "1", func(p *corev1.Pod) { p.Status.Phase = corev1.PodPending }), "not running"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r, _ := newRT("n1", c.pod)
			w := workload(t, r, "pod:"+string(c.pod.UID))
			got, listed := w.Can[runtime.ActionCPULimit]
			if !listed || got != c.want {
				t.Fatalf("Can = %q (listed %v), want %q", got, listed, c.want)
			}
		})
	}
}

func TestMultiContainerPodsCannotBeResized(t *testing.T) {
	p := burstable("m", "n1", "um", "100m", "1")
	p.Spec.Containers = append(p.Spec.Containers, corev1.Container{Name: "d", Resources: corev1.ResourceRequirements{Limits: rl("1")}})
	r, _ := newRT("n1", p)
	if _, err := r.Apply(context.Background(), "pod:um", runtime.Change{Action: runtime.ActionCPULimit, CPULimit: 0.3}); err == nil {
		t.Fatal("applied to a two-container pod")
	}
}

func TestPodsNotRunningAreListed(t *testing.T) {
	r, _ := newRT("n1", burstable("a", "n1", "ua", "100m", "1", func(p *corev1.Pod) { p.Status.Phase = corev1.PodSucceeded }))
	if w := workload(t, r, "pod:ua"); w.Can[runtime.ActionCPULimit] != "not running" {
		t.Fatalf("can %v", w.Can)
	}
}

func TestTheAgentsOwnPodIsSelf(t *testing.T) {
	t.Setenv("POD_UID", "ua")
	r, _ := newRT("n1", burstable("a", "n1", "ua", "100m", "1"), burstable("b", "n1", "ub", "100m", "1"))
	if !workload(t, r, "pod:ua").Self || workload(t, r, "pod:ub").Self {
		t.Fatal("only pod ua is the agent's own")
	}
}

func TestNoPodUIDMeansNoSelf(t *testing.T) {
	t.Setenv("POD_UID", "")
	r, _ := newRT("n1", burstable("a", "n1", "", "100m", "1"))
	if workload(t, r, "pod:").Self {
		t.Fatal("an empty POD_UID must not match a pod with an empty UID")
	}
}

func TestTheFactoryNamesTheSpecPath(t *testing.T) {
	_, err := runtime.Build("kubernetes", runtime.Env{NodeID: "n1", Spec: spec.RuntimeSpec{Kind: "kubernetes", Kubeconfig: "/nonexistent/kubeconfig"}})
	if err == nil || !strings.Contains(err.Error(), "runtime.kubeconfig") {
		t.Fatalf("err %v; want it to name runtime.kubeconfig", err)
	}
}

func TestRevertWithNoAnnotationRestoresTheOriginal(t *testing.T) {
	r, cs := newRT("n1", burstable("a", "n1", "ua", "100m", "300m"))
	u := runtime.Undo{NodeID: "n1", Subject: "pod:ua", Original: runtime.Resource{Limit: f64(1)}}
	if err := r.Revert(context.Background(), u); err != nil {
		t.Fatal(err)
	}
	if got := getPod(t, cs, "a").Spec.Containers[0].Resources.Limits.Cpu().String(); got != "1" {
		t.Fatalf("limit %s, want 1", got)
	}
}
