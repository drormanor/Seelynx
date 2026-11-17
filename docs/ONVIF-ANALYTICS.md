# ONVIF analytics events

How cameras report analytics events over ONVIF, as seen on real cameras. Topic and field
names vary by model, which is what drivers are for.

## How events arrive

Tested on a Vicon V2002D-W28IR. An analytic must be turned on in the camera's own settings
before it fires. A real walk across a Line Cross line sent:

    tns1:RuleEngine/LineDetector/Crossed  Value=true  token=VAToken

Each crossing sends one `Value=true`, and no `false` follows.

The camera sends the current state of every topic when anything subscribes, marked
`PropertyOperation="Initialized"`. It repeats the last crossing as `Value=true`, so these must
not be treated as events.

## Topics listed by the V2002D-W28IR

All with `Value` and `token=VAToken` unless noted:

| Analytic | Topic |
|---|---|
| Line Cross | `tns1:RuleEngine/LineDetector/Crossed` |
| Intrusion | `tns1:RuleEngine/FieldDetector/ObjectsInside` |
| Entered | `tns1:VideoAnalytics/tnsht:ObjectEntered/Entered` |
| Exited | `tns1:VideoAnalytics/tnsht:ObjectExited/Exited` |
| Loitering | `tns1:VideoAnalytics/tnsht:LoiteringDetector/ObjectIsLoitering` |
| Crowd | `tns1:VideoAnalytics/tnsht:CrowdDetector/Crowded` |
| Face | `tns1:VideoAnalytics/tnsht:FaceDetector/Face` |
| Object left | `tns1:VideoAnalytics/tnsht:ObjectLeftDetector/ObjectIsLeft` |
| Object removed | `tns1:VideoAnalytics/tnsht:ObjectRemoveDetector/ObjectIsRemoved` |
| Wrong direction | `tns1:VideoAnalytics/tnsht:ObjectMoveDetector/ObjectIsWrongDirection` |
| Audio | `tns1:AudioAnalytics/Audio/DetectedSound` (`Source`, `State`) |

Only Line Cross has been seen firing. The others are listed by the camera but not yet tested.

## Other models

- **V934D-IR:** reports `tns1:VideoAnalytics/tnsht:LineDetector/Crossed`, `.../Loitering/ObjectIsLoitering`,
  `.../ObjectClass/Human` and `.../ObjectClass/Car` with a lowercase `state` field, and
  `tns1:RuleEngine/FieldDetector/ObjectsInside` with `IsInside`. Not yet seen firing.
- **V2105MD-W28IR:** lists no analytics events.
- **Another camera with object classification:** real traffic fired
  `tns1:VideoAnalytics/tnsht:ObjectClass/Car` with `Value=true`, then `Value=false` a few
  seconds later. It also sends plain motion as `tns1:RuleEngine/MotionRegionDetector/Motion`
  with `State`, alongside `tns1:VideoSource/MotionAlarm`.
