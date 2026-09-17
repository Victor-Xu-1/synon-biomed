"""Collision-safe layout of contact-supported schematic pocket boundaries.

The rounded local footprint is a 2D proximity envelope, NOT a measured protein
surface. Unlike a convex hull it preserves bays between ligand branches.
"""

from __future__ import annotations

import math
import re
import xml.etree.ElementTree as ET
from functools import lru_cache

Point = tuple[float, float]
WALL_CLEARANCE = 26.0
FOOTPRINT_RADIUS = 60.0
GRID_STEP = 4.0
SAMPLE_STEP = 3.0
CONTACT_HALF_ANGLE = math.radians(34.0)


def drawing_points(body: str, offset: Point) -> list[Point]:
    """Include RDKit glyph/control points, not only atom centres, in clearance."""
    root = ET.fromstring(f"<svg>{body}</svg>")
    points = []
    for element in root.iter():
        data = element.get("d")
        if not data:
            continue
        # RDKit emits absolute M/L/Q/C paths. Reject unsupported geometry
        # instead of accidentally interpreting radii/flags as coordinates.
        commands = re.findall(r"[A-DF-Za-df-z]", data)
        if any(command not in {"M", "L", "Q", "C", "Z"} for command in commands):
            raise ValueError("unsupported ligand drawing path for pocket clearance")
        values = [float(value) for value in re.findall(r"[-+]?(?:\d*\.\d+|\d+)(?:[eE][-+]?\d+)?", data)]
        if len(values) % 2:
            raise ValueError("invalid ligand drawing path for pocket clearance")
        # Sample each individual SVG subpath, never connect separate glyphs.
        # Control polygons conservatively contain the small RDKit glyph curves.
        for subpath in re.split(r"(?=M)", data):
            coordinates = [float(v) for v in re.findall(r"[-+]?(?:\d*\.\d+|\d+)(?:[eE][-+]?\d+)?", subpath)]
            vertices = [(coordinates[i] + offset[0], coordinates[i + 1] + offset[1])
                        for i in range(0, len(coordinates), 2)]
            if any(not math.isfinite(v) or abs(v) > 4096 for p in vertices for v in p):
                raise ValueError("ligand drawing exceeds bounded layout coordinates")
            points.extend(vertices)
            for first, second in zip(vertices, vertices[1:]):
                count = max(1, math.ceil(math.dist(first, second) / 10.0))
                points.extend((first[0] + (second[0] - first[0]) * step / count,
                               first[1] + (second[1] - first[1]) * step / count)
                              for step in range(1, count))
            if len(points) > 60000:
                raise ValueError("ligand drawing exceeds bounded path sample count")
    return points


def angular_distance(first: float, second: float) -> float:
    return abs((first - second + math.pi) % math.tau - math.pi)


def resample_loop(points: list[Point], spacing: float) -> list[Point]:
    """Uniform arc-length samples avoid grid-dependent corners and dot spacing."""
    result, remaining = [points[0]], spacing
    for first, second in zip(points, points[1:] + points[:1]):
        length = math.dist(first, second)
        while length >= remaining and length > 1e-9:
            first = (first[0] + (second[0] - first[0]) * remaining / length,
                     first[1] + (second[1] - first[1]) * remaining / length)
            result.append(first)
            length = math.dist(first, second)
            remaining = spacing
        remaining -= length
    if math.dist(result[-1], result[0]) < spacing * .5:
        result.pop()
    return result


@lru_cache(maxsize=8)
def _local_contours(seeds: tuple[Point, ...]) -> tuple[tuple[Point, ...], ...]:
    """Outline the union of local drawing buffers; discard only interior holes.

    Raster topology gives one unambiguous closed exterior per component. Bounded
    corner cutting smooths it without the overshoot of an interpolating spline.
    Its 60px buffer leaves room above the 26px glyph clearance invariant.
    """
    cells = set()
    radius = FOOTPRINT_RADIUS / GRID_STEP
    for px, py in seeds:
        x, y = px / GRID_STEP, py / GRID_STEP
        for row in range(math.floor(y - radius), math.ceil(y + radius)):
            dy = row + .5 - y
            if abs(dy) >= radius:
                continue
            dx = math.sqrt(radius * radius - dy * dy)
            cells.update((col, row) for col in range(math.ceil(x - dx - .5), math.floor(x + dx - .5) + 1))
        if len(cells) > 250000:
            raise ValueError("pocket footprint exceeds bounded cell limit")
    edges = {}
    for x, y in cells:
        for neighbour, start, end in (
            ((x,y-1),(x,y),(x+1,y)), ((x+1,y),(x+1,y),(x+1,y+1)),
            ((x,y+1),(x+1,y+1),(x,y+1)), ((x-1,y),(x,y+1),(x,y)),
        ):
            if neighbour not in cells:
                edges.setdefault(start, set()).add(end)
    contours = []
    while edges:
        start = min(edges)
        current, loop = start, [start]
        while True:
            choices = edges[current]
            if len(choices) > 1 and len(loop) > 1:
                dx, dy = current[0] - loop[-2][0], current[1] - loop[-2][1]
                # At diagonal contacts follow the right-hand cell boundary.
                following = max(choices, key=lambda p: dx * (p[1]-current[1]) - dy * (p[0]-current[0]))
            else:
                following = min(choices)
            choices.remove(following)
            if not choices:
                del edges[current]
            current = following
            if current == start:
                break
            loop.append(current)
        area = sum(a[0]*b[1]-a[1]*b[0] for a,b in zip(loop,loop[1:]+loop[:1]))
        if area <= 0:  # Ring holes are ligand interior, not pocket boundaries.
            continue
        points = resample_loop([(x*GRID_STEP,y*GRID_STEP) for x,y in loop], 32.0)
        for _ in range(4):
            points = [p for a,b in zip(points,points[1:]+points[:1])
                      for p in ((.75*a[0]+.25*b[0],.75*a[1]+.25*b[1]),
                                (.25*a[0]+.75*b[0],.25*a[1]+.75*b[1]))]
        contours.append(tuple(resample_loop(points, SAMPLE_STEP)))
    return tuple(sorted(contours, key=len, reverse=True))


def boundary_contours(exclusion_points: list[Point]) -> tuple[tuple[Point, ...], ...]:
    """Bounded, cached local outlines shared by contact walls and solvent gaps."""
    if not exclusion_points:
        return ()
    if len(exclusion_points) > 60000 or any(not math.isfinite(v) or abs(v) > 4096 for p in exclusion_points for v in p):
        raise ValueError("pocket drawing exceeds bounded layout coordinates")
    # Quantization avoids recalculating identical buffers for dense glyph paths.
    seeds = tuple(sorted({(round(x / 2) * 2., round(y / 2) * 2.) for x,y in exclusion_points}))
    return _local_contours(seeds)


def boundary_segments(
    exclusion_points: list[Point],
    center: Point,
    contact_angles: list[float],
    openings: list[Point],
    label_centres: list[Point],
    opening_clearance: float = 42.0,
) -> list[list[Point]]:
    """Keep locally supported arcs, with explicit solvent and label gaps."""
    contours = boundary_contours(exclusion_points)
    if not contours or not contact_angles:
        return []

    def visible(point: Point) -> bool:
        angle = math.atan2(point[1] - center[1], point[0] - center[0])
        return (
            32 < point[0] < 1568 and 32 < point[1] < 894
            and any(angular_distance(angle, contact) <= CONTACT_HALF_ANGLE for contact in contact_angles)
            and all(math.dist(point, opening) >= opening_clearance for opening in openings)
            # Rotated petal control points fit within radius 68; include stroke.
            and all(math.dist(point, label) >= 74 for label in label_centres)
        )

    segments = []
    for contour in contours:
        contour = list(contour)
        flags = [visible(point) for point in contour]
        if not all(flags):
            split = flags.index(False)
            contour = contour[split:] + contour[:split]
            flags = flags[split:] + flags[:split]
        else:
            contour = contour + [contour[0]]
            flags = flags + [True]
        current = []
        for point, keep in zip(contour, flags):
            if keep:
                current.append(point)
            elif current:
                segments.append(current)
                current = []
        if current:
            segments.append(current)
    # Skip tiny fragments rather than stretching them across unsupported space.
    return [segment for segment in segments
            if sum(math.dist(a, b) for a, b in zip(segment, segment[1:])) >= 60]


def boundary_paths(
    exclusion_points: list[Point], center: Point, contact_angles: list[float],
    openings: list[Point], label_centres: list[Point], opening_clearance: float = 42.0,
) -> list[str]:
    return ["M " + " L ".join(f"{x:.2f} {y:.2f}" for x, y in segment)
            for segment in boundary_segments(exclusion_points, center, contact_angles, openings, label_centres, opening_clearance)]


def solvent_boundary_segments(
    exclusion_points: list[Point], atom_points: dict[int, Point],
    accessible_directions: dict[int, list[Point]], label_centres: list[Point],
) -> list[list[Point]]:
    """Project local accessible patches, not one global region representative.

    A contour sample belongs to its nearest depicted atom. Only measured
    directions on that atom can support an opening. Directions pointing into
    another branch cannot paint the far side of the molecule. Hidden 3D
    directions are omitted upstream, never reversed or replaced by a radial
    guess. This is a 2D projection of accessibility, not a 3D channel geometry.
    """
    segments = []
    for raw in boundary_contours(exclusion_points):
        contour = list(raw)
        flags = []
        for point in contour:
            atom = min(atom_points, key=lambda i: math.dist(point, atom_points[i]))
            origin = atom_points[atom]
            dx, dy = point[0]-origin[0], point[1]-origin[1]
            length = math.hypot(dx,dy)
            directions = accessible_directions.get(atom, [])
            visible = (0 < length < 120 and 32 < point[0] < 1568 and 32 < point[1] < 894
                       and all(math.dist(point,label)>=74 for label in label_centres))
            flags.append(visible and any((dx*x+dy*y)/length >= math.cos(math.radians(20))
                                         for x,y in directions))
        if not all(flags):
            split = flags.index(False)
            contour, flags = contour[split:]+contour[:split], flags[split:]+flags[:split]
        else:
            contour, flags = contour+[contour[0]], flags+[True]
        current = []
        for point, keep in zip(contour, flags):
            if keep:
                current.append(point)
            elif current:
                segments.append(current)
                current = []
        if current:
            segments.append(current)
    return [s for s in segments if sum(math.dist(a,b) for a,b in zip(s,s[1:])) >= 12]


def solvent_halos(atom_points: dict[int, Point], exposure: list[dict]) -> str:
    """Depict every computed exposed atom locally, never invent a water/channel.

    The 0.08 threshold is the analysis contract for partial exposure. Radius and
    opacity encode probe-accessible fraction, not interaction energy or charge.
    Draw behind the ligand so heteroatom labels retain their element colors.
    """
    markup = []
    for item in exposure:
        atom = int(item["atom_index"])
        fraction = float(item["accessible_fraction"])
        if atom not in atom_points or not math.isfinite(fraction) or not 0 <= fraction <= 1:
            raise ValueError("invalid atom solvent exposure for depiction")
        if fraction < .08:
            continue
        x, y = atom_points[atom]
        radius, opacity = 28 + 24 * math.sqrt(fraction), .16 + .65 * math.sqrt(fraction)
        markup.append(
            f'<circle cx="{x:.2f}" cy="{y:.2f}" r="{radius:.2f}" '
            f'fill="url(#ligand-exposure)" opacity="{opacity:.3f}" '
            f'data-exposure-atom="{atom}" data-solvent-exposure="{fraction:.4f}">'
            f'<title>Atom {atom + 1} · bulk-connected probe accessibility {fraction:.1%}</title></circle>'
        )
    return "".join(markup)


def solvent_opening_band(segment: list[Point], drawing: list[Point], labels: list[Point],
                         atoms: list[int], regions: list[int]) -> str:
    """A translucent outside patch beside an actual gap, not a replacement wall."""
    outer = []
    for i, point in enumerate(segment):
        before, after = segment[max(0,i-1)], segment[min(len(segment)-1,i+1)]
        dx, dy = after[0]-before[0], after[1]-before[1]
        length = max(1e-9,math.hypot(dx,dy))
        # Positive-oriented external contours have the molecular interior on
        # their right. Extrude left, taper at ends, and avoid another branch.
        width = 28 * math.sqrt(max(0,math.sin(math.pi*i/(len(segment)-1))))
        target = (point[0]+dy/length*width,point[1]-dx/length*width)
        while width > 1 and (min(math.dist(target,p) for p in drawing) < 32
                             or any(math.dist(target,p)<74 for p in labels)):
            width *= .5
            target = (point[0]+dy/length*width,point[1]-dx/length*width)
        outer.append(target)
    path = 'M '+ ' L '.join(f'{x:.2f} {y:.2f}' for x,y in segment+list(reversed(outer)))+' Z'
    atom_text = ' '.join(str(i+1) for i in atoms)
    region_text = ' '.join(str(i+1) for i in regions)
    return (f'<path d="{path}" fill="#97d5eb" fill-opacity="0.35" stroke="none" '
            f'data-solvent-atoms="{atom_text}" data-solvent-regions="{region_text}">'
            f'<title>Solvent region {region_text}; atoms {atom_text}. Local projected patch, '
            'not a separate biological mouth or physical channel width.</title></path>')
