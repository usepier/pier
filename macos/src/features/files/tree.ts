// A flat list of paths as a folder tree, folders first.

export interface Node {
  name: string;
  path: string;
  children?: Node[]; // folders only
}

export function buildTree(paths: string[]): Node[] {
  const root: Node = { name: "", path: "", children: [] };
  const dirs = new Map<string, Node>([["", root]]);
  const dir = (path: string): Node => {
    let d = dirs.get(path);
    if (d) return d;
    const i = path.lastIndexOf("/");
    const parent = dir(i < 0 ? "" : path.slice(0, i));
    d = { name: path.slice(i + 1), path, children: [] };
    parent.children!.push(d);
    dirs.set(path, d);
    return d;
  };
  for (const p of paths) {
    const i = p.lastIndexOf("/");
    dir(i < 0 ? "" : p.slice(0, i)).children!.push({ name: p.slice(i + 1), path: p });
  }
  const sort = (n: Node) => {
    n.children?.sort((a, b) => (a.children ? 0 : 1) - (b.children ? 0 : 1) || a.name.localeCompare(b.name));
    n.children?.forEach(sort);
  };
  sort(root);
  return root.children!;
}
