# merge_tasks merges a list of override tasks into a base list of tasks.
#
# Rules:
#   - If an override's name matches a base task, it replaces in place
#     (preserving the base task's position).
#   - If an override has remove=True, the matching base task is removed.
#   - Otherwise the override is appended to the end.
#
# This lets units add or replace named tasks without restating the class's
# default task list.
def merge_tasks(base, overrides):
    if not overrides:
        return list(base)
    result = list(base)
    for o in overrides:
        name = o.name
        idx = -1
        for i in range(len(result)):
            if result[i].name == name:
                idx = i
                break
        if getattr(o, "remove", False):
            if idx >= 0:
                result.pop(idx)
            continue
        if idx >= 0:
            result[idx] = o
        else:
            result.append(o)
    return result
